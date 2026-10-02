"""Loopback-network ECR simulator and allowlisted CONNECT proxy. Fake credentials only."""
import base64, http.server, json, socket, ssl, threading, select, time
from pathlib import Path
class ECR(http.server.BaseHTTPRequestHandler):
    def log_message(self,*args): pass
    def do_POST(self):
        self.rfile.read(int(self.headers.get('Content-Length',0)))
        expired=Path('/lab/expired').exists()
        body=json.dumps({'authorizationData':[{'authorizationToken':base64.b64encode(b'AWS:lab-token').decode(),'expiresAt':1 if expired else int(time.time()+43200)}]}).encode()
        self.send_response(200);self.send_header('Content-Type','application/x-amz-json-1.1');self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
        print(json.dumps({'event':'GetAuthorizationToken','expired_response':expired,'time':time.time()}),flush=True)
class Proxy(http.server.BaseHTTPRequestHandler):
    def log_message(self,*args): pass
    def do_CONNECT(self):
        if self.path!='api.ecr.us-east-1.amazonaws.com:443': self.send_error(403);return
        with socket.create_connection(('127.0.0.1',8443)) as dst:
            self.send_response(200);self.end_headers()
            sockets=[self.connection,dst]
            while True:
                ready,_,_=select.select(sockets,[],[],45)
                if not ready:return
                for src in ready:
                    data=src.recv(65536)
                    if not data:return
                    (dst if src is self.connection else self.connection).sendall(data)
srv=http.server.ThreadingHTTPServer(('0.0.0.0',8443),ECR)
ctx=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);ctx.load_cert_chain('/lab/ca.pem','/lab/key.pem');srv.socket=ctx.wrap_socket(srv.socket,server_side=True)
threading.Thread(target=srv.serve_forever,daemon=True).start()
http.server.ThreadingHTTPServer(('0.0.0.0',8080),Proxy).serve_forever()
