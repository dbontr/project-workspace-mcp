"""GitHub repository broker. Provider credentials stay on the controller."""
import base64
import json
import os
import re
import uuid

import httpx

REPO = re.compile(r'^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$')
PATH = re.compile(r'^[A-Za-z0-9_.-][A-Za-z0-9_./+@ -]{0,1023}$')
MAX_ARCHIVE = 12 * 1024 * 1024


class GitHubBroker:
    def __init__(self, token, client=None):
        self.token = token
        self.client = client or httpx.Client(base_url='https://api.github.com', timeout=35)

    def _request(self, method, url, *, body=None, params=None):
        if not self.token:
            raise ValueError('PWM_GITHUB_TOKEN is not configured')
        response = self.client.request(method, url, headers={
            'Authorization': 'Bearer ' + self.token,
            'Accept': 'application/vnd.github+json',
            'X-GitHub-Api-Version': '2022-11-28',
            'User-Agent': 'project-workspace-mcp',
        }, json=body, params=params)
        if response.status_code >= 400:
            raise ValueError('GitHub API returned ' + str(response.status_code) + ': ' + response.text[:400])
        return response.json() if response.content else None

    @staticmethod
    def _repo(full_name):
        if not isinstance(full_name,str) or not REPO.fullmatch(full_name):
            raise ValueError('Invalid GitHub repository')
        return '/repos/' + full_name

    @staticmethod
    def _path(path):
        if not isinstance(path,str) or not PATH.fullmatch(path) or any(part in ('.', '..') for part in path.split('/')):
            raise ValueError('Invalid repository file path')
        return path

    def api_request(self, full_name, method, endpoint, params=None, payload=None):
        """Perform a bounded GitHub REST request under one project-linked repository."""
        self._repo(full_name)
        if method not in ('GET', 'POST', 'PUT', 'PATCH', 'DELETE'):
            raise ValueError('Unsupported GitHub method')
        if (not isinstance(endpoint, str) or len(endpoint) > 512 or
                not re.fullmatch(r'[A-Za-z0-9_.@+-]+(?:/[A-Za-z0-9_.@+-]+)*', endpoint) or
                any(segment in ('.', '..') for segment in endpoint.split('/'))):
            raise ValueError('Invalid repository API endpoint')
        if not isinstance(params, (dict, type(None))) or len(params or {}) > 16:
            raise ValueError('Invalid GitHub query parameters')
        if not isinstance(payload, (dict, list, type(None))) or len(json.dumps(payload).encode()) > 256_000:
            raise ValueError('GitHub request body exceeds limit')
        if params is not None and any(not isinstance(key,str) or
                                      not re.fullmatch(r'[A-Za-z_][A-Za-z0-9_-]{0,63}', key) or
                                      not isinstance(value,(str,int,bool)) or
                                      len(str(value)) > 1024
                                      for key,value in params.items()):
            raise ValueError('Invalid GitHub query parameters')
        if method == 'GET' and payload is not None:
            raise ValueError('GET requests cannot contain a body')
        if method != 'GET' and os.getenv('PWM_GITHUB_GENERIC_WRITES') != '1':
            raise ValueError('Generic GitHub mutations require PWM_GITHUB_GENERIC_WRITES=1')
        # All routes remain under the linked repository; account and org APIs are excluded.
        return self._request(method, self._repo(full_name) + '/' + endpoint,
                             body=payload, params=params)

    def repo(self, full_name):
        result=self._request('GET', self._repo(full_name))
        return {'id':result['id'],'full_name':result['full_name'],
                'default_branch':result['default_branch'],'private':result['private']}

    def commit(self, full_name, ref):
        self._repo(full_name)
        if not re.fullmatch(r'[A-Za-z0-9_./-]{1,160}',ref) or '..' in ref:
            raise ValueError('Invalid Git reference')
        return self._request('GET', f'/repos/{full_name}/commits/{ref}')['sha']

    def file(self, full_name, path, ref):
        self._repo(full_name)
        from urllib.parse import quote
        path=self._path(path)
        result=self._request('GET', f'/repos/{full_name}/contents/{quote(path,safe="/")}?ref={quote(ref,safe="")}')
        if result.get('type') != 'file' or result.get('encoding') != 'base64':
            raise ValueError('GitHub did not return a base64-encoded file')
        data=base64.b64decode(result['content'])
        if len(data)>131072:raise ValueError('File exceeds 128 KiB limit')
        return {'repo':full_name,'path':path,'sha':result['sha'],
                'content':data.decode('utf-8'),'revision':ref}

    def issues(self,full_name,state='open'):
        self._repo(full_name)
        if state not in ('open','closed','all'):raise ValueError('Invalid state')
        records=self._request('GET',f'/repos/{full_name}/issues?state={state}&per_page=50')
        return [{'number':r['number'],'title':r['title'],'state':r['state'],'url':r['html_url']}
                for r in records if 'pull_request' not in r]

    def pull_requests(self,full_name,state='open'):
        self._repo(full_name)
        if state not in ('open','closed','all'):raise ValueError('Invalid state')
        records=self._request('GET',f'/repos/{full_name}/pulls?state={state}&per_page=50')
        return [{'number':r['number'],'title':r['title'],'state':r['state'],'url':r['html_url'],
                 'draft':r['draft']} for r in records]

    def issue_create(self,full_name,title,body=''):
        self._repo(full_name)
        if not title.strip() or len(title)>256 or len(body)>20000:
            raise ValueError('Invalid issue')
        result=self._request('POST',f'/repos/{full_name}/issues',body={'title':title,'body':body})
        return {'number':result['number'],'url':result['html_url']}

    def snapshot(self, full_name, ref):
        """Download one immutable GitHub archive, without forwarding credentials to redirects."""
        self._repo(full_name)
        sha = self.commit(full_name,ref)
        # Do not follow redirects with an Authorization header; follow only trusted GitHub archive URLs.
        headers={'Authorization':'Bearer '+self.token,'Accept':'application/vnd.github+json',
                 'X-GitHub-Api-Version':'2022-11-28', 'User-Agent':'project-workspace-mcp'}
        first=self.client.get(f'/repos/{full_name}/tarball/{sha}',headers=headers,follow_redirects=False)
        if first.status_code not in (301,302,303,307,308):
            raise ValueError('GitHub tarball endpoint did not return an archive redirect')
        from urllib.parse import urlparse
        location=first.headers.get('location','')
        parsed=urlparse(location)
        if parsed.scheme!='https' or parsed.hostname not in ('codeload.github.com','api.github.com'):
            raise ValueError('Unexpected GitHub archive host')
        with httpx.Client(timeout=60,follow_redirects=False) as client:
            with client.stream('GET',location,headers={'User-Agent':'project-workspace-mcp'}) as stream:
                stream.raise_for_status()
                chunks=[];size=0
                for chunk in stream.iter_bytes():
                    size+=len(chunk)
                    if size>MAX_ARCHIVE:raise ValueError('Repository archive exceeds 12 MiB')
                    chunks.append(chunk)
        return sha,b''.join(chunks)

    def publish(self,full_name,base_branch,expected_sha,files,title,body=''):
        """Create a new branch and draft PR without rewriting existing branches."""
        self._repo(full_name)
        if not isinstance(files,dict) or not 1 <= len(files) <= 16:
            raise ValueError('Supply between 1 and 16 modified files')
        if not re.fullmatch(r'[A-Za-z0-9_./-]{1,160}',base_branch) or '..' in base_branch:
            raise ValueError('Invalid base branch')
        if not re.fullmatch(r'[a-f0-9]{40}',expected_sha):raise ValueError('Invalid expected commit SHA')
        if not isinstance(title,str) or not 0 < len(title.strip()) <= 256:
            raise ValueError('Invalid PR title')
        from urllib.parse import quote
        base=self._request('GET',f'/repos/{full_name}/git/ref/heads/{quote(base_branch,safe="/")}')['object']['sha']
        if base!=expected_sha:raise ValueError('Version conflict: GitHub base branch moved')
        tree=self._request('GET',f'/repos/{full_name}/git/commits/{base}')['tree']['sha']
        entries=[]
        for path, data in sorted(files.items()):
            path=self._path(path)
            if isinstance(data,str):data=data.encode()
            if not isinstance(data,bytes) or len(data)>1_048_576:
                raise ValueError('Changed file must be at most 1 MiB')
            blob=self._request('POST',f'/repos/{full_name}/git/blobs',body={
                'content':base64.b64encode(data).decode(),'encoding':'base64'})
            entries.append({'path':path,'mode':'100644','type':'blob','sha':blob['sha']})
        new_tree=self._request('POST',f'/repos/{full_name}/git/trees',body={'base_tree':tree,'tree':entries})['sha']
        new_commit=self._request('POST',f'/repos/{full_name}/git/commits',body={
            'message':title,'tree':new_tree,'parents':[expected_sha]})['sha']
        branch='pwm/'+uuid.uuid4().hex[:16]
        self._request('POST',f'/repos/{full_name}/git/refs',body={'ref':'refs/heads/'+branch,'sha':new_commit})
        review=self._request('POST',f'/repos/{full_name}/pulls',body={
            'title':title,'head':branch,'base':base_branch,'body':body,'draft':True})
        return {'url':review['html_url'],'number':review['number'],'branch':branch,
                'commit_sha':new_commit,'base_sha':expected_sha,'draft':True}
