"""Controller HTTP authorization and pairing through the actual MCP SDK application."""
import importlib
import sys

import pytest
from fastapi.testclient import TestClient

pytest.importorskip('mcp')


def test_admin_and_worker_credentials_are_separate(monkeypatch,tmp_path):
    monkeypatch.setenv('PWM_ADMIN_TOKEN','administrator-secret-'+'x'*48)
    monkeypatch.setenv('PWM_DB',str(tmp_path/'controller.db'))
    sys.modules.pop('project_workspace.server',None)
    server=importlib.import_module('project_workspace.server')
    with TestClient(server.app, base_url="http://localhost") as client:
        assert client.get('/api/status').status_code==401
        admin_headers={'Authorization':'Bearer administrator-secret-'+'x'*48}
        created=client.post('/api/projects',headers=admin_headers,
                            json={'id':'demo','title':'Demo'}).json()
        assert created['id']=='demo'
        code=client.post('/api/pairing',headers=admin_headers).json()['pairing_code']
        paired=client.post('/api/pair',json={'pairing_code':code,'name':'node-one',
                           'capabilities':{'cpu':2,'memory_mb':2048,'max_jobs':1,'docker':True}})
        assert paired.status_code==200
        token=paired.json()['worker_token']
        assert client.post('/api/pair',json={'pairing_code':code,'name':'node-two',
                           'capabilities':{'cpu':2,'memory_mb':2048,'max_jobs':1}}).status_code==400
        worker_headers={'Authorization':'Bearer '+token}
        assert client.post('/api/worker/node-one/heartbeat',headers=worker_headers,
                           json={'capabilities':{'cpu':2,'memory_mb':2048,'max_jobs':1,'docker':True}}).status_code==200
        assert client.get('/api/projects',headers=worker_headers).status_code==401
        assert client.post('/api/worker/node-two/claim',headers=worker_headers).status_code==401
