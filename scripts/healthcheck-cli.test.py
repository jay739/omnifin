"""Exercise the actual executable without opening config or starting the application."""
import http.server
import os
from pathlib import Path
import subprocess
import tempfile
import threading

class Handler(http.server.BaseHTTPRequestHandler):
    status = 200
    def do_GET(self):
        self.send_response(self.status);self.end_headers();self.wfile.write(b'{"status":"ok","jellyfin":"unavailable"}')
    def log_message(self,*args):pass

server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Handler)
thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
binary=str(Path('build/omnifin').resolve())
try:
    with tempfile.TemporaryDirectory() as directory:
        env=dict(os.environ,HOME=directory,XDG_CONFIG_HOME=directory,OMNIFIN_HEALTH_URL=f'http://127.0.0.1:{server.server_port}/health')
        for flag in ['-healthcheck','--healthcheck']:
            result=subprocess.run([binary,flag],env=env,cwd=directory,capture_output=True,text=True,timeout=5)
            assert result.returncode==0,(result.stdout,result.stderr)
            assert result.stdout=='',result.stdout
        Handler.status=503
        result=subprocess.run([binary,'-healthcheck'],env=env,cwd=directory,capture_output=True,text=True,timeout=5)
        assert result.returncode==1,result
        assert list(Path(directory).iterdir())==[], 'Probe wrote application state'
finally:
    server.shutdown();server.server_close()
print('Executable health probe checks passed')
