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
    store.node_register('worker-one',{'cpu':2,'memory_mb':2048,'max_jobs':1})
    job=store.job_submit('alpha','echo ok')
    leased=store.claim('worker-one',{'cpu':2,'memory_mb':2048,'max_jobs':1})
    assert leased['id']==job['id']
    with pytest.raises(ValueError):store.finish('worker-one',job['id'],'bad','succeeded','oops')
    assert store.claim('worker-one',{'cpu':2,'memory_mb':2048,'max_jobs':1}) is None
    assert store.finish('worker-one',job['id'],leased['lease_token'],'succeeded','ok')['status']=='succeeded'
    assert store.jobs('alpha')[0]['result']=='ok'
