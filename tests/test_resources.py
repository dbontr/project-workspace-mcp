"""Environment versions, physical device fencing, and durable workspace snapshots."""

import base64
import hashlib
import sqlite3

import pytest

from project_workspace.core import Store
from project_workspace.device_config import docker_args, manifest


@pytest.fixture
def store(tmp_path):
    result = Store(tmp_path/'state.db')
    result.project_create('alpha','Alpha')
    result.project_create('beta','Beta')
    result.node_register('node-one',{'cpu':8,'memory_mb':16384,'max_jobs':2,'docker':True,
                                    'devices':[{'id':'gpu-a','kind':'gpu','model':'RTX'},
                                               {'id':'npu-a','kind':'npu'},
                                               {'id':'fpga-a','kind':'fpga'}]})
    return result


def ready(store,**kwargs):
    ws=store.workspace_create('alpha',**kwargs)
    op=store.operation_claim(ws['node'])
    assert op['action']=='start'
    store.operation_finish(ws['node'],op['id'],op['lease_token'],True,{'container':'pwm-test'})
    return ws


def test_devices_exclusively_reserved_and_released_after_ack(store):
    first=ready(store,hardware={'gpu':1,'fpga':1})
    status=store.hardware_inventory()
    assert {d['id'] for d in status if d['leased_to']==first['id']}=={'gpu-a','fpga-a'}
    with pytest.raises(ValueError,match='capacity and hardware'):
        store.workspace_create('alpha',hardware={'fpga':1})
    second=ready(store,hardware={'npu':1})
    assert {d['id'] for d in store.hardware_inventory() if d['leased_to']==second['id']}=={'npu-a'}
    requested=store.workspace_stop('alpha',first['id'])
    assert requested['status']=='stopping'
    with pytest.raises(ValueError,match='capacity and hardware'):
        store.workspace_create('alpha',hardware={'fpga':1})
    stop=store.operation_claim('node-one')
    store.operation_finish('node-one',stop['id'],stop['lease_token'],False,{'error':'offline'})
    assert next(d for d in store.hardware_inventory() if d['id']=='fpga-a')['leased_to']==first['id']
    # A failed cleanup can be retried; leases remain fenced until confirmed.
    store.workspace_stop('alpha',first['id'])
    op=store.operation_claim('node-one')
    store.operation_finish('node-one',op['id'],op['lease_token'],True,{'status':'stopped'})
    assert next(d for d in store.hardware_inventory() if d['id']=='fpga-a')['leased_to'] is None
    assert store.workspace_create('alpha',hardware={'fpga':1})['hardware'][0]['id']=='fpga-a'


def test_bad_hardware_inventory_rejected(store):
    with pytest.raises(ValueError,match='duplicate'):
        store.node_register('bad-node',{'cpu':2,'memory_mb':1024,'max_jobs':1,
            'devices':[{'id':'gpu-a','kind':'gpu'},{'id':'gpu-a','kind':'gpu'}]})
    with pytest.raises(ValueError,match='Hardware request'):
        store.workspace_create('alpha',hardware={'tpu':1})
    with pytest.raises(ValueError,match='Invalid hardware'):
        store.workspace_create('alpha',hardware={'fpga':-1})


def test_environment_build_version_and_node_affinity(store):
    build=store.environment_define('alpha','simulation','python:3.12-slim',
        'FROM python:3.12-slim\nRUN python -V\n',0)
    assert build['version']==1
    with pytest.raises(ValueError,match='version conflict'):
        store.environment_define('alpha','simulation','python:3.12-slim','',0)
    with pytest.raises(ValueError,match='not ready'):
        store.workspace_create('alpha',environment='simulation')
    ws=ready(store)
    requested=store.environment_build('alpha','simulation',ws['id'])
    assert requested['status']=='queued'
    op=store.operation_claim('node-one')
    assert op['action']=='build_image'
    store.operation_finish('node-one',op['id'],op['lease_token'],True,
                           {'image':build['image'],'image_id':'sha256:abc'})
    assert store.environments('alpha')[0]['status']=='ready'
    using=store.workspace_create('alpha',environment='simulation')
    assert using['node']=='node-one'
    assert store.workspace_get('alpha',using['id'])['image']==build['image']
    assert store.environments('beta')==[]
    next_=store.environment_define('alpha','simulation','python:3.12-slim','',1)
    assert next_['version']==2
    assert store.workspace_get('alpha',using['id'])['image']==build['image']


def test_snapshot_artifact_project_scope_and_restore(store):
    ws=ready(store)
    queued=store.workspace_call('alpha',ws['id'],'snapshot',{})
    op=store.operation_claim('node-one')
    blob=b'archive bytes'
    # Streaming operation upload is bound to the claim, not to a general admin credential.
    assert store.upload_authorize('node-one',ws['id'],op['id'],op['lease_token'])=='alpha'
    with pytest.raises(ValueError,match='Invalid upload lease'):
        store.upload_authorize('node-one',ws['id'],op['id'],'wrong')
    import tempfile
    from pathlib import Path
    (Path(store.path).parent/'artifacts').mkdir(exist_ok=True)
    with tempfile.NamedTemporaryFile(delete=False,dir=Path(store.path).parent/'artifacts') as out:
        out.write(blob)
        pathname=out.name
    artifact=store.upload_commit('node-one',ws['id'],op['id'],op['lease_token'],
                                'workspace.tar.gz',pathname,len(blob),hashlib.sha256(blob).hexdigest())
    result=store.operation_finish('node-one',op['id'],op['lease_token'],True,{'artifact_id':artifact['id']})
    assert result['result']['artifact']['sha256']==hashlib.sha256(blob).hexdigest()
    snap=store.workspace_snapshots('alpha',ws['id'])[0]
    assert store.artifact_get('alpha',snap['artifact'])[1]==blob
    with pytest.raises(ValueError,match='not ready'):
        store.workspace_restore('beta',ws['id'],snap['id'])
    copied=ready(store)
    restored=store.workspace_restore('alpha',copied['id'],snap['id'])
    op=store.operation_claim('node-one')
    assert op['action']=='restore_snapshot' and op['payload']['artifact_id']==artifact['id']
    assert restored['status']=='queued'
    assert store.worker_artifact('node-one',copied['id'],artifact['id'])[1]==blob


def test_stream_upload_fenced_after_completion(store,tmp_path):
    ws=ready(store)
    call=store.workspace_call('alpha',ws['id'],'export_file',{'path':'notes.txt'})
    op=store.operation_claim('node-one')
    content=b'hello'
    output=tmp_path/'upload'
    output.write_bytes(content)
    artifact=store.upload_commit('node-one',ws['id'],op['id'],op['lease_token'],
                                 'notes.txt',output,len(content),hashlib.sha256(content).hexdigest())
    with pytest.raises(ValueError,match='already'):
        store.upload_commit('node-one',ws['id'],op['id'],op['lease_token'],
                            'notes.txt',tmp_path/'not-present',len(content),hashlib.sha256(content).hexdigest())
    store.operation_finish('node-one',op['id'],op['lease_token'],True,{'artifact_id':artifact['id']})
    with pytest.raises(ValueError,match='Invalid upload lease'):
        store.upload_authorize('node-one',ws['id'],op['id'],op['lease_token'])


def test_device_policy_does_not_accept_arbitrary_paths(tmp_path,monkeypatch):
    policy=tmp_path/'devices.json'
    policy.write_text('{"devices": [{"id":"gpu-1","kind":"gpu","cdi":"nvidia.com/gpu=GPU-test"},'
                      '{"id":"fpga-1","kind":"fpga","paths":["/dev/null"]}]}')
    configured=manifest(policy)
    assert docker_args([{'id':'gpu-1','kind':'gpu'}],configured)==['--device','nvidia.com/gpu=GPU-test']
    assert docker_args([{'id':'fpga-1','kind':'fpga'}],configured)==['--device','/dev/null']
    with pytest.raises(ValueError,match='lease'):
        docker_args([{'id':'fpga-2','kind':'fpga'}],configured)
    policy.write_text('{"devices":[{"id":"evil","kind":"npu","paths":["/etc/passwd"]}]}')
    with pytest.raises(ValueError,match='path'):
        manifest(policy)


def test_environment_image_can_be_exported_and_loaded_on_another_node(store,tmp_path):
    definition=store.environment_define('alpha','vivado','python:3.12-slim','',0)
    source=ready(store)
    store.environment_build('alpha','vivado',source['id'])
    built=store.operation_claim('node-one')
    store.operation_finish('node-one',built['id'],built['lease_token'],True,
                           {'image':definition['image'],'image_id':'sha256:imagerevision'})
    assert store.environments('alpha')[0]['status']=='ready'
    exported=store.environment_export('alpha','vivado',source['id'])
    transfer=store.operation_claim('node-one')
    image_tar=b'not a real image; storage and lease semantics only'
    tmp=tmp_path/'image.tar'
    tmp.write_bytes(image_tar)
    receipt=store.upload_commit('node-one',source['id'],transfer['id'],transfer['lease_token'],
                                'environment.tar',tmp,len(image_tar),hashlib.sha256(image_tar).hexdigest())
    store.operation_finish('node-one',transfer['id'],transfer['lease_token'],True,
                           {'artifact_id':receipt['id']})
    store.node_register('node-two',{'cpu':4,'memory_mb':8192,'max_jobs':1,'docker':True})
    # Fill node-one to force the destination workspace onto the second worker.
    occupying=store.workspace_create('alpha',cpu=7,memory_mb=8000)
    assert occupying['node']=='node-one'
    destination=store.workspace_create('alpha',cpu=2,memory_mb=1024)
    assert destination['node']=='node-two'
    # Operation queues are per worker; the outstanding start on node-one is harmless.
    start=store.operation_claim('node-two')
    store.operation_finish('node-two',start['id'],start['lease_token'],True,
                           {'status':'ready'})
    transfer=store.environment_import('alpha','vivado',destination['id'],receipt['id'])
    loaded=store.operation_claim('node-two')
    assert loaded['action']=='load_image' and loaded['payload']['artifact_id']==receipt['id']
    assert store.worker_artifact('node-two',destination['id'],receipt['id'])[1]==image_tar
    store.operation_finish('node-two',loaded['id'],loaded['lease_token'],True,
                           {'image':definition['image'],'image_id':'sha256:imagerevision'})
    assert store.environments('alpha')[0]['node']=='node-two'
    using=store.workspace_create('alpha',environment='vivado',cpu=1,memory_mb=512)
    assert using['node']=='node-two'
    with pytest.raises(ValueError,match='not associated'):
        store.environment_import('alpha','vivado',destination['id'],'0'*32)


def test_usb_passthrough_requires_exact_device_and_both_toggles(store):
    store.node_register('node-two', {'cpu':4,'memory_mb':8192,'max_jobs':1,'docker':True,
        'devices':[{'id':'usb-drive-a','kind':'usb','model':'USB flash drive'},
                   {'id':'usb-drive-b','kind':'usb','model':'USB flash drive'}]})
    with pytest.raises(ValueError,match='explicitly enabled'):
        store.workspace_create('alpha',hardware={'usb':1})
    with pytest.raises(ValueError,match='exact device ID'):
        store.workspace_create('alpha',usb_passthrough=True,usb_node='node-two')
    with pytest.raises(ValueError,match='selected worker'):
        store.workspace_create('alpha',usb_passthrough=True,usb_device_id='usb-drive-a')
    with pytest.raises(ValueError,match='capacity and hardware'):
        store.workspace_create('alpha',usb_passthrough=True,usb_node='node-one',usb_device_id='usb-drive-a')
    first=ready(store,usb_passthrough=True,usb_node='node-two',usb_device_id='usb-drive-b')
    assert first['node']=='node-two' and first['hardware']==[{'id':'usb-drive-b','kind':'usb'}]
    assert next(d for d in store.hardware_inventory() if d['id']=='usb-drive-b')['leased_to']==first['id']
    with pytest.raises(ValueError,match='capacity and hardware'):
        store.workspace_create('alpha',usb_passthrough=True,usb_node='node-two',usb_device_id='usb-drive-b')
    assert store.workspace_create('alpha',usb_passthrough=True,usb_node='node-two',usb_device_id='usb-drive-a')['hardware'][0]['id']=='usb-drive-a'
    store.workspace_stop('alpha',first['id'])
    with pytest.raises(ValueError,match='capacity and hardware'):
        store.workspace_create('alpha',usb_passthrough=True,usb_node='node-two',usb_device_id='usb-drive-b')
