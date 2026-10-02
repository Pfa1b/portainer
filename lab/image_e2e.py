"""Run only against the named disposable Portainer/DinD lab, never production."""
import json,secrets,time,urllib.request,urllib.error
from pathlib import Path
BASE='http://astra-portainer-fixed:9000/api'
token=None
def request(method,path,body=None,multipart=False):
    headers={}
    if token:headers['Authorization']='Bearer '+token
    if multipart:
        boundary='lab-boundary';data=b''
        for k,v in body.items():data+=f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n'.encode()
        data+=f'--{boundary}--\r\n'.encode();headers['Content-Type']='multipart/form-data; boundary='+boundary
    elif body is not None:data=json.dumps(body).encode();headers['Content-Type']='application/json'
    else:data=None
    try:
        with urllib.request.urlopen(urllib.request.Request(BASE+path,method=method,headers=headers,data=data),timeout=40) as r:
            raw=r.read();return r.status,json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:raise RuntimeError(f'{method} {path} HTTP={e.code}') from None
password=secrets.token_urlsafe(24)
request('POST','/users/admin/init',{'Username':'labadmin','Password':password})
_,auth=request('POST','/auth',{'Username':'labadmin','Password':password});token=auth['jwt']
_,endpoint=request('POST','/endpoints',{'Name':'isolated-dind','EndpointCreationType':'1','URL':'tcp://astra-portainer-dind:2375','TLS':'false'},True)
eid=endpoint['Id']
_,reg=request('POST','/registries',{'Name':'fake-ecr','Type':7,'URL':'lab.invalid','Authentication':True,'Username':'fake-access','Password':'fake-secret','Ecr':{'Region':'us-east-1'}})
compose="services:\n  app:\n    image: alpine:3.20\n    pull_policy: never\n    command: [sleep, '3600']\n"
_,stack=request('POST',f'/stacks/create/standalone/string?endpointId={eid}',{'Name':'astra-image-e2e','StackFileContent':compose,'Env':[{'name':'LAB_REVISION','value':'old'}]})
sid=stack['Id']
_,beforeReg=request('GET',f'/registries/{reg["Id"]}')
assert beforeReg['AccessTokenExpiry']==1, 'fixture must persist an expired token before PUT'
_,before=request('GET',f'/endpoints/{eid}/docker/containers/json')
before={x['Id']:x['ImageID'] for x in before if any('astra-image-e2e' in n for n in x['Names'])}
assert len(before)==1
Path('/lab/expired').unlink()
new=compose+'# metadata-only lab update\n';start=time.monotonic()
status,_=request('PUT',f'/stacks/{sid}?endpointId={eid}',{'StackFileContent':new,'Env':[{'name':'LAB_REVISION','value':'new'}],'PullImage':False})
duration=time.monotonic()-start
_,metadata=request('GET',f'/stacks/{sid}');_,file=request('GET',f'/stacks/{sid}/file')
_,afterReg=request('GET',f'/registries/{reg["Id"]}')
_,after=request('GET',f'/endpoints/{eid}/docker/containers/json')
after={x['Id']:x['ImageID'] for x in after if any('astra-image-e2e' in n for n in x['Names'])}
assert status==200 and duration<10
assert metadata['Env']==[{'name':'LAB_REVISION','value':'new'}]
assert file['StackFileContent']==new and before==after
assert afterReg['AccessTokenExpiry']>time.time()
print(json.dumps({'PUT_HTTP':status,'PUT_DURATION_SECONDS':round(duration,3),'STACK_METADATA_UPDATED':True,'COMPOSE_UPDATED':True,'CONTAINER_STATE':'running','RUNTIME_UNCHANGED':before==after,'TOKEN_EXPIRY_BEFORE':1,'TOKEN_EXPIRY_AFTER_FUTURE':True,'NO_DEADLOCK':True,'STACK_ID':sid,'ENDPOINT_ID':eid,'CONTAINER_IDS':list(after)},indent=2))
