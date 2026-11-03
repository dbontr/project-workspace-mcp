"""Controller, pairing, and worker command-line interface."""
import argparse
import os
import secrets
from pathlib import Path

import httpx

from .worker import capabilities, serve


def _config_dir():
    return Path(os.getenv('PWM_CONFIG_DIR', str(Path.home()/'.config/project-workspace')))


def _save_secret(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if path.exists():raise ValueError('Credential file already exists: ' + str(path))
    fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
    with os.fdopen(fd,'w',encoding='utf-8') as f:
        f.write(value+'\n')


def _admin_token():
    if os.getenv('PWM_ADMIN_TOKEN'):
        return os.environ['PWM_ADMIN_TOKEN']
    path=_config_dir()/'admin.token'
    if not path.is_file():raise ValueError('Run project-workspace init or set PWM_ADMIN_TOKEN')
    return path.read_text(encoding='utf-8').strip()


def _controller_url(value):
    if not (value.startswith('https://') or value.startswith('http://127.0.0.1:')):
        raise ValueError('Controller must use HTTPS or loopback HTTP')
    return value.rstrip('/')


def _api(controller, path, payload, token=None):
    headers={'Authorization':'Bearer '+token} if token else {}
    with httpx.Client(base_url=_controller_url(controller),headers=headers,timeout=25) as client:
        response=client.post(path,json=payload)
        response.raise_for_status()
        return response.json()


def main():
    parser=argparse.ArgumentParser(prog='project-workspace')
    sub=parser.add_subparsers(dest='operation',required=True)
    sub.add_parser('init',help='Create a local administrator credential')
    controller=sub.add_parser('controller',help='Start the controller and MCP service')
    controller.add_argument('--host',default='127.0.0.1')
    controller.add_argument('--port',type=int,default=8000)
    pair=sub.add_parser('pair',help='Create a one-time worker enrollment code')
    pair.add_argument('--controller',default='http://127.0.0.1:8000')
    join=sub.add_parser('join',help='Enroll this machine using a pairing code')
    join.add_argument('--controller',required=True)
    join.add_argument('--node',required=True)
    join.add_argument('--code',required=True)
    worker=sub.add_parser('worker',help='Start a paired worker')
    worker.add_argument('--controller',required=True)
    worker.add_argument('--node',required=True)
    worker.add_argument('--token',default=os.getenv('PWM_WORKER_TOKEN'))
    sub.add_parser('doctor',help='Inspect available worker resources')
    args=parser.parse_args()
    try:
        if args.operation=='init':
            path=_config_dir()/'admin.token'
            _save_secret(path,secrets.token_urlsafe(48))
            print('Administrator credential saved to '+str(path))
        elif args.operation=='doctor':
            print(capabilities())
        elif args.operation=='pair':
            result=_api(args.controller,'/api/pairing',{},_admin_token())
            print('One-time code: '+result['pairing_code'])
            print('Expires in '+str(result['expires_in_seconds'])+' seconds')
        elif args.operation=='join':
            result=_api(args.controller,'/api/pair',{'pairing_code':args.code,
                    'name':args.node,'capabilities':capabilities()})
            path=_config_dir()/'workers'/(args.node+'.token')
            _save_secret(path,result['worker_token'])
            print('Worker enrolled. Credential saved to '+str(path))
        elif args.operation=='controller':
            os.environ['PWM_ADMIN_TOKEN']=_admin_token()
            import uvicorn
            uvicorn.run('project_workspace.server:app',host=args.host,port=args.port)
        elif args.operation=='worker':
            token=args.token or (_config_dir()/'workers'/(args.node+'.token')).read_text().strip()
            serve(args.controller,args.node,token)
    except (OSError,ValueError,httpx.HTTPError) as exc:
        parser.exit(1,str(exc)+'\n')


if __name__=='__main__':
    main()
