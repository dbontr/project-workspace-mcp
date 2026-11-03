"""Mocked repository-scoped GitHub operations and expected-revision publication."""
import base64
import json

import httpx
import pytest

from project_workspace.github import GitHubBroker


def broker_for(handler):
    transport=httpx.MockTransport(handler)
    return GitHubBroker('test-secret',httpx.Client(base_url='https://api.github.com',transport=transport))


def test_git_file_read_and_authenticated_request():
    def handler(request):
        assert request.headers['Authorization']=='Bearer test-secret'
        assert request.url.path=='/repos/example/repo/contents/src/test.py'
        return httpx.Response(200,json={'type':'file','encoding':'base64','sha':'b'*40,
                                        'content':base64.b64encode(b'print(1)').decode()})
    got=broker_for(handler).file('example/repo','src/test.py','a'*40)
    assert got['content']=='print(1)'
    with pytest.raises(ValueError,match='Invalid GitHub repository'):
        broker_for(handler).file('https://evil/x','test','a'*40)


def test_draft_pr_requires_exact_base_revision():
    base='a'*40
    def handler(request):
        if request.url.path.endswith('/git/ref/heads/main'):
            return httpx.Response(200,json={'object':{'sha':base}})
        pytest.fail('Unexpected write on mismatched expected SHA')
    with pytest.raises(ValueError,match='Version conflict'):
        broker_for(handler).publish('example/repo','main','b'*40,{'x.txt':b'x'},'Title')


def test_draft_pr_publishes_separate_branch_without_force_push():
    sent=[]
    def handler(request):
        sent.append((request.method,request.url.path))
        path=request.url.path
        if path.endswith('/git/ref/heads/main'):
            return httpx.Response(200,json={'object':{'sha':'a'*40}})
        if path.endswith('/git/commits/'+'a'*40):
            return httpx.Response(200,json={'tree':{'sha':'b'*40}})
        if path.endswith('/git/blobs'):
            payload=json.loads(request.content)
            assert base64.b64decode(payload['content'])==b'edited'
            return httpx.Response(201,json={'sha':'c'*40})
        if path.endswith('/git/trees'):
            assert json.loads(request.content)['base_tree']=='b'*40
            return httpx.Response(201,json={'sha':'d'*40})
        if path.endswith('/git/commits'):
            assert json.loads(request.content)['parents']==['a'*40]
            return httpx.Response(201,json={'sha':'e'*40})
        if path.endswith('/git/refs'):
            assert json.loads(request.content)['ref'].startswith('refs/heads/pwm/')
            return httpx.Response(201,json={'ref':'ok'})
        if path.endswith('/pulls'):
            assert json.loads(request.content)['draft'] is True
            return httpx.Response(201,json={'html_url':'https://github.com/example/repo/pull/2','number':2})
        pytest.fail(path)
    result=broker_for(handler).publish('example/repo','main','a'*40,{'src/one.txt':b'edited'},'Fix tests')
    assert result['draft'] and result['number']==2
    assert not any(method=='PATCH' for method,_ in sent)
