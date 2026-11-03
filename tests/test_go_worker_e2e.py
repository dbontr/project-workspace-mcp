"""Real Go worker and Python MCP controller over HTTP with Docker execution."""

import json
import hashlib
import os
import signal
import socket
import subprocess
import sys
import time
from pathlib import Path

import httpx
import pytest


pytestmark = pytest.mark.skipif(
    os.getenv('RUN_GO_INTEGRATION') != '1' or not os.getenv('PWM_GO_WORKER'),
    reason='Requires a Linux Docker runner and built Go worker executable',
)


def until(fn, timeout=100, interval=0.25):
    start=time.monotonic()
    last=None
    while time.monotonic()-start<timeout:
        try:
            result=fn()
            if result:return result
        except (httpx.HTTPError,KeyError) as exc:
            last=exc
        time.sleep(interval)
    pytest.fail(f'Operation did not complete within test limit: {last}')


def test_go_agent_hardware_environment_and_artifact_roundtrip(tmp_path):
    worker_binary=os.environ['PWM_GO_WORKER']
    with socket.socket() as sock:
        sock.bind(('127.0.0.1',0))
        port=sock.getsockname()[1]
    controller=f'http://127.0.0.1:{port}'
    token='integration-owner-'+'q'*48
    env=os.environ.copy()
    env['PWM_DB']=str(tmp_path/'controller.db')
    env['PWM_ADMIN_TOKEN']=token
    env['PWM_ALLOWED_HOSTS']='127.0.0.1,localhost'
    env['PWM_ALLOWED_ORIGINS']='http://127.0.0.1,http://localhost'
    env['PWM_MAX_ARTIFACT_BYTES']=str(1024*1024*1024)
    manifest=tmp_path/'devices.json'
    manifest.write_text(json.dumps({'devices':[
        {'id':'npu-test','kind':'npu','model':'simulated character-device lease','paths':['/dev/null']}
    ]}))
    env['PWM_DEVICE_MANIFEST']=str(manifest)
    saved_token=tmp_path/'worker.token'
    server=subprocess.Popen([sys.executable,'-m','uvicorn','project_workspace.server:app',
                             '--host','127.0.0.1','--port',str(port),'--log-level','error'],
                             env=env,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
    go_agent=None
    workspaces=[]
    with httpx.Client(base_url=controller,timeout=30) as client:
        admin={'Authorization':'Bearer '+token}
        def api(method,path,**kwargs):
            response=client.request(method,path,headers=admin,**kwargs)
            assert response.status_code in (200,201),f'{path}: {response.status_code}: {response.text[:1000]}'
            return response.json()

        def workspace_state(wid,status='ready'):
            def read():
                matches=[w for w in api('GET','/api/projects/integration/workspaces')
                         if w['id']==wid]
                return matches[0] if matches and matches[0]['status']==status else None
            return until(read)

        def operation(wid,action,payload=None):
            response=api('POST',f'/api/projects/integration/workspaces/{wid}/operations',
                         json={'action':action,'payload':payload or {}})
            op_id=response['operation_id']
            observed={}
            def read():
                if go_agent is not None and go_agent.poll() is not None:
                    log=go_agent.stderr.read().decode('utf-8','replace')[-4000:]
                    pytest.fail(f'Go worker exited during {action}: {log}')
                snapshot=api('GET',f'/api/projects/integration/workspaces/{wid}/operations/{op_id}')
                observed.update(snapshot)
                if snapshot['status']=='failed':pytest.fail(f'Worker {action} failed: {snapshot["result"]}')
                return snapshot if snapshot['status']=='succeeded' else None
            try:
                return until(read,timeout=75)['result']
            except BaseException:
                if go_agent is not None and go_agent.poll() is not None:
                    print('Go worker stopped:',go_agent.stderr.read().decode('utf-8','replace')[-4000:])
                print('Pending operation:',observed)
                raise

        def stop(wid):
            api('POST',f'/api/projects/integration/workspaces/{wid}/stop')
            workspace_state(wid,'stopped')

        try:
            until(lambda:client.get('/health').status_code==200)
            api('POST','/api/projects',json={'id':'integration','title':'Go integration'})
            pairing=api('POST','/api/pairing')['pairing_code']
            subprocess.run([worker_binary,'join','--controller',controller,'--node','go-worker',
                            '--code',pairing,'--devices',str(manifest),'--token-file',str(saved_token)],
                           check=True,env=env,timeout=30,capture_output=True)
            go_agent=subprocess.Popen([worker_binary,'serve','--controller',controller,
                                      '--node','go-worker','--devices',str(manifest),
                                      '--token-file',str(saved_token)],
                                     env=env,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
            until(lambda:any(n['id']=='go-worker' and n['online'] for n in api('GET','/api/nodes')))
            source=api('POST','/api/workspaces',json={'project_id':'integration',
                         'cpu':1,'memory_mb':512,'hardware':{'npu':1}})
            wid=source['id'];workspaces.append(wid)
            workspace_state(wid)
            assert source['hardware'][0]=={'id':'npu-test','kind':'npu'}
            inventory=api('GET','/api/hardware')
            assert inventory[0]['leased_to']==wid
            operation(wid,'write_file',{'path':'answer.py','content':'print(42)\n'})
            out=operation(wid,'command',{'command':'python answer.py'})
            assert out['exit_code']==0 and out['stdout'].strip()=='42'
            snapshot=operation(wid,'snapshot')
            assert snapshot['artifact']['sha256'] and snapshot['artifact']['size']>0
            snapshots=api('GET',f'/api/projects/integration/workspaces/{wid}/snapshots')
            assert len(snapshots)==1
            stop(wid)
            assert api('GET','/api/hardware')[0]['leased_to'] is None
            cloned=api('POST','/api/workspaces',json={'project_id':'integration','cpu':1,
                                                    'memory_mb':512})
            clone_id=cloned['id'];workspaces.append(clone_id)
            workspace_state(clone_id)
            restoration=api('POST',f'/api/projects/integration/workspaces/{clone_id}/snapshots/{snapshots[0]["id"]}/restore')
            restore_id=restoration['operation_id']
            until(lambda:(lambda op:op if op['status']=='succeeded' else None)(api(
                'GET',f'/api/projects/integration/workspaces/{clone_id}/operations/{restore_id}')),timeout=180)
            assert operation(clone_id,'read_file',{'path':'answer.py'})['content']=='print(42)\n'

            # Stream an image-like artifact larger than the 128 KiB text-write limit.
            image_bytes=os.urandom(320*1024)
            image_sha=hashlib.sha256(image_bytes).hexdigest()
            delivered=client.put('/api/projects/integration/artifacts/upload',content=image_bytes,
                                 headers={**admin,'X-Artifact-Name':'os-image.bin',
                                          'X-Content-Sha256':image_sha})
            assert delivered.status_code==200, delivered.text
            uploaded=delivered.json()
            imported=api('POST',f'/api/projects/integration/workspaces/{clone_id}/artifacts/import',
                         json={'artifact_id':uploaded['id'],'path':'images/os-image.bin'})
            def import_file_done():
                op=api('GET',f'/api/projects/integration/workspaces/{clone_id}/operations/{imported["operation_id"]}')
                if op['status']=='failed':pytest.fail(f'Artifact import failed: {op["result"]}')
                return op if op['status']=='succeeded' else None
            until(import_file_done,timeout=180)
            checked=operation(clone_id,'command',{'command':'sha256sum images/os-image.bin'})
            assert checked['exit_code']==0 and checked['stdout'].split()[0]==image_sha

            recipe={'name':'python-tools','base_image':'python:3.12-slim',
                    'dockerfile':'FROM python:3.12-slim\nRUN python --version\n',
                    'expected_version':0}
            definition=api('POST','/api/projects/integration/environments',json=recipe)
            built=api('POST',f'/api/projects/integration/environments/python-tools/build/{clone_id}')
            def build_done():
                op=api('GET',f'/api/projects/integration/workspaces/{clone_id}/operations/{built["operation_id"]}')
                if op['status']=='failed':pytest.fail(f'Environment build failed: {op["result"]}')
                return op if op['status']=='succeeded' else None
            until(build_done,timeout=180)
            environments=api('GET','/api/projects/integration/environments')
            assert environments[0]['status']=='ready'
            exported=api('POST',f'/api/projects/integration/environments/python-tools/export/{clone_id}')
            def export_done():
                op=api('GET',f'/api/projects/integration/workspaces/{clone_id}/operations/{exported["operation_id"]}')
                if op['status']=='failed':pytest.fail(str(op['result']))
                return op if op['status']=='succeeded' else None
            image_artifact=until(export_done,timeout=240)['result']['artifact']
            assert image_artifact['size']>10000
            imported=api('POST',f'/api/projects/integration/environments/python-tools/import/{clone_id}',
                         json={'artifact_id':image_artifact['id']})
            def import_done():
                op=api('GET',f'/api/projects/integration/workspaces/{clone_id}/operations/{imported["operation_id"]}')
                if op['status']=='failed':pytest.fail(str(op['result']))
                return op if op['status']=='succeeded' else None
            until(import_done,timeout=240)
            stop(clone_id)
            specialized=api('POST','/api/workspaces',json={'project_id':'integration',
                'environment':'python-tools','cpu':1,'memory_mb':512})
            specialized_id=specialized['id'];workspaces.append(specialized_id)
            workspace_state(specialized_id)
            assert operation(specialized_id,'command',{'command':'python --version'})['exit_code']==0
            stop(specialized_id)
        finally:
            if go_agent:
                go_agent.send_signal(signal.SIGTERM)
                try:go_agent.communicate(timeout=15)
                except subprocess.TimeoutExpired:go_agent.kill();go_agent.communicate()
            server.send_signal(signal.SIGTERM)
            try:server.communicate(timeout=15)
            except subprocess.TimeoutExpired:server.kill();server.communicate()
            for workspace in workspaces:
                name='pwm-'+workspace
                subprocess.run(['docker','rm','-f',name],check=False,capture_output=True)
                subprocess.run(['docker','volume','rm',name],check=False,capture_output=True)
            subprocess.run(['docker','image','rm','pwm-env-integration-python-tools:v1'],
                           check=False,capture_output=True)
