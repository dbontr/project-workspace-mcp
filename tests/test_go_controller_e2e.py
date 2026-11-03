"""Cross-language project knowledge compatibility through the Go controller."""

import os
import socket
import subprocess
import time
from pathlib import Path

import httpx
import pytest

from project_workspace.core import Store

pytestmark = pytest.mark.skipif(
    os.getenv('RUN_GO_CONTROLLER_TEST') != '1',
    reason='requires the Go controller executable',
)


def test_go_controller_shares_authoritative_sqlite_state(tmp_path):
    binary = os.environ['PWM_GO_CONTROLLER']
    path = tmp_path / 'state.db'
    store = Store(path)
    store.project_create('python-project', 'Python project')
    store.note_put('python-project', 'context', 'Created by Python', 0)
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    token = 'integration-' + 'k' * 48
    process = subprocess.Popen(
        [binary, '--listen', f'127.0.0.1:{port}', '--db', str(path)],
        env={**os.environ, 'PWM_ADMIN_TOKEN': token},
        stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
    )
    try:
        with httpx.Client(base_url=f'http://127.0.0.1:{port}', timeout=5) as client:
            for _ in range(100):
                try:
                    if client.get('/health').status_code == 200:
                        break
                except httpx.HTTPError:
                    pass
                time.sleep(0.05)
            else:
                pytest.fail('Go controller did not start')
            headers = {'Authorization': 'Bearer ' + token}
            assert client.get('/api/projects').status_code == 401
            assert client.get('/api/projects', headers=headers).json()[0]['id'] == 'python-project'
            note = client.get('/api/projects/python-project/notes/context', headers=headers)
            assert note.status_code == 200 and note.json()['body'] == 'Created by Python'
            update = client.post('/api/projects/python-project/notes/context', headers=headers,
                                 json={'body': 'Written through Go', 'expected_version': 1})
            assert update.status_code == 200, update.text
            assert store.note_get('python-project', 'context')['body'] == 'Written through Go'
            assert store.note_get('python-project', 'context')['version'] == 2
            created = client.post('/api/projects', headers=headers,
                                  json={'id': 'go-project', 'title': 'Go project'})
            assert created.status_code == 200, created.text
            assert any(project['id'] == 'go-project' for project in store.projects())
            saved = store.note_put('go-project', 'result', 'Stored by Python', 0)
            assert saved['version'] == 1
            response = client.get('/api/projects/go-project/notes/result', headers=headers)
            assert response.status_code == 200 and response.json()['body'] == 'Stored by Python'
    finally:
        process.terminate()
        try:
            process.communicate(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.communicate(timeout=5)
