"""No host-file access, shell interpolation, or unsafe archive import."""
import io
import tarfile
from types import SimpleNamespace

import pytest

import project_workspace.worker as worker


def test_relative_paths_reject_traversal():
    for name in ('/etc/passwd','../etc/passwd','dir/../escape','', 'a/./b','a\x00b'):
        with pytest.raises(ValueError):worker._path(name)
    assert worker._path('dir/file.txt')=='/workspace/dir/file.txt'


def test_job_runs_without_network_host_mounts_or_privileges(monkeypatch):
    calls=[]
    monkeypatch.setattr(worker.platform,'system',lambda:'Linux')
    monkeypatch.setattr(worker.shutil,'which',lambda x:'/usr/bin/docker')
    def fake_run(args,**kwargs):
        calls.append(args)
        return SimpleNamespace(returncode=0,stdout='ok',stderr='')
    monkeypatch.setattr(worker.subprocess,'run',fake_run)
    state,result=worker.execute({'image':'python:3.12-slim','cpu':1,'memory_mb':256,'command':'echo ok'})
    assert state=='succeeded'
    assert '--network' in calls[0] and 'none' in calls[0]
    assert '--read-only' in calls[0] and '--cap-drop' in calls[0]
    assert '--privileged' not in calls[0] and '--volume' not in calls[0]
    with pytest.raises(ValueError,match='not approved'):
        worker.execute({'image':'evil:latest','cpu':1,'memory_mb':256,'command':'echo x'})


def test_workspace_file_write_quotes_shell_path_and_uses_stdin(monkeypatch):
    runner=worker.WorkspaceRunner()
    calls=[]
    def mock(*args,data=None,timeout=120):
        calls.append((args,data))
        return SimpleNamespace(returncode=0,stdout=b'',stderr=b'')
    monkeypatch.setattr(runner,'_docker',mock)
    response=runner.handle({'workspace':'a'*32,'action':'write_file',
                            'payload':{'path':'hello;rm file.txt','content':'contents'}})
    assert response['sha256']
    assert calls[0][1]==b'contents'
    assert "'/workspace/hello;rm file.txt'" in calls[0][0][-1]


def test_reject_unsafe_repository_archive_before_docker_copy(monkeypatch):
    bad=io.BytesIO()
    with tarfile.open(fileobj=bad,mode='w:gz') as archive:
        item=tarfile.TarInfo('repo-main/../../secret.txt')
        data=b'danger'
        item.size=len(data)
        archive.addfile(item,io.BytesIO(data))
    runner=worker.WorkspaceRunner(lambda *_:bad.getvalue())
    monkeypatch.setattr(runner,'_docker',lambda *a,**k:pytest.fail('Docker must not receive unsafe archive'))
    with pytest.raises(ValueError,match='unsafe'):
        runner.handle({'workspace':'a'*32,'action':'import_archive',
                       'payload':{'repository':'example/repo','artifact_id':'b'*32,'revision':'c'*40}})
