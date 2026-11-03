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



def test_mcp_initialize_and_tool_discovery_require_admin_credential(monkeypatch, tmp_path):
    monkeypatch.setenv('PWM_ADMIN_TOKEN', 'administrator-secret-' + 'z' * 48)
    monkeypatch.setenv('PWM_DB', str(tmp_path / 'mcp.db'))
    sys.modules.pop('project_workspace.server', None)
    server = importlib.import_module('project_workspace.server')
    headers = {
        'Authorization': 'Bearer administrator-secret-' + 'z' * 48,
        'Content-Type': 'application/json',
        'Accept': 'application/json, text/event-stream',
        'MCP-Protocol-Version': '2025-06-18',
    }
    initialize = {
        'jsonrpc': '2.0',
        'id': 1,
        'method': 'initialize',
        'params': {
            'protocolVersion': '2025-06-18',
            'capabilities': {},
            'clientInfo': {'name': 'project-workspace-test', 'version': '1'},
        },
    }
    with TestClient(server.app, base_url='http://localhost') as client:
        unauthorized = client.post('/mcp/', json=initialize,
                                   headers={k: v for k, v in headers.items() if k != 'Authorization'})
        assert unauthorized.status_code == 401
        authorized = client.post('/mcp/', json=initialize, headers=headers)
        assert authorized.status_code == 200, authorized.text
        response = authorized.json()
        assert 'result' in response and 'serverInfo' in response['result']
        discovered = client.post('/mcp/', json={
            'jsonrpc': '2.0', 'id': 2, 'method': 'tools/list', 'params': {},
        }, headers=headers)
        assert discovered.status_code == 200, discovered.text
        names = {tool['name'] for tool in discovered.json()['result']['tools']}
        assert {'workspace_create', 'workspace_call', 'github_checkout_repository'} <= names
