import pytest
from project_workspace.core import Store

@pytest.fixture
def store(tmp_path):return Store(tmp_path/'state.db')

def test_project_notes_and_conflicts(store):
    store.project_create('alpha','Alpha')
    assert store.note_put('alpha','context','hello',0)['version']==1
    assert store.note_put('alpha','context','updated',1)['version']==2
    assert store.note_get('alpha','context')['body']=='updated'
    assert store.search('alpha','updated')[0]['name']=='context'
    with pytest.raises(ValueError):store.note_put('alpha','context','stale',1)
    with pytest.raises(ValueError):store.note_put('alpha','context','blind')

def test_scoped_notes(store):
    store.project_create('alpha','Alpha')
    store.project_create('beta','Beta')
    store.note_put('alpha','context','private',0)
    assert store.search('beta','private')==[]
    with pytest.raises(ValueError):store.note_get('beta','context')

def test_pairing_revokes_old_token(store):
    token=store.node_register('worker-one',{'cpu':2,'memory_mb':4096,'max_jobs':1})
    assert store.node_auth('worker-one',token)
    replacement=store.node_register('worker-one',{'cpu':2,'memory_mb':4096,'max_jobs':1})
    assert not store.node_auth('worker-one',token)
    assert store.node_auth('worker-one',replacement)

def test_job_lease_fencing(store):
    store.project_create('alpha','Alpha')
    store.node_register('worker-one',{'cpu':2,'memory_mb':2048,'max_jobs':1,'docker':True})
    job=store.job_submit('alpha','echo ok')
    leased=store.claim('worker-one',{'cpu':2,'memory_mb':2048,'max_jobs':1,'docker':True})
    assert leased['id']==job['id']
    with pytest.raises(ValueError):store.finish('worker-one',job['id'],'bad','succeeded','oops')
    assert store.claim('worker-one',{'cpu':2,'memory_mb':2048,'max_jobs':1,'docker':True}) is None
    assert store.finish('worker-one',job['id'],leased['lease_token'],'succeeded','ok')['status']=='succeeded'
    assert store.jobs('alpha')[0]['result']=='ok'


def test_one_shot_jobs_cannot_overbook_workspace_capacity(store):
    store.project_create('alpha','Alpha')
    caps={'cpu':2,'memory_mb':2048,'max_jobs':2,'docker':True}
    store.node_register('worker-one',caps)
    reserved=store.workspace_create('alpha',cpu=1.5,memory_mb=1536)
    assert reserved['node']=='worker-one'
    large=store.job_submit('alpha','echo large',cpu=1,memory_mb=512)
    assert store.claim('worker-one',caps) is None
    small=store.job_submit('alpha','echo small',cpu=0.5,memory_mb=512)
    leased=store.claim('worker-one',caps)
    assert leased is not None and leased['id']==small['id']
    assert store.claim('worker-one',caps) is None
    store.finish('worker-one',leased['id'],leased['lease_token'],'succeeded','ok')
    assert store.claim('worker-one',caps) is None


def test_non_docker_workers_do_not_receive_container_jobs(store):
    store.project_create('alpha','Alpha')
    store.node_register('worker-one',{'cpu':8,'memory_mb':8192,'max_jobs':1,'docker':False})
    store.job_submit('alpha','echo ok')
    assert store.claim('worker-one',{'cpu':8,'memory_mb':8192,'max_jobs':1,'docker':False}) is None
