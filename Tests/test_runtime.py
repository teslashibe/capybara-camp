"""Exercise real child processes using local fake CLIs, without provider calls."""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest

BRIDGE=Path(__file__).parents[1]/"Resources/bridge.py"
BOOTSTRAP='''import importlib.util,json,sys
spec=importlib.util.spec_from_file_location("bridge",sys.argv[1]);b=importlib.util.module_from_spec(spec);spec.loader.exec_module(b)
s=json.loads(sys.argv[2]);b.runtime_command=lambda *args: [sys.executable,"-u","-c",sys.argv[3]]
try:b.chat(s,"hello from camp",speed=s.get("_test_speed",""))
except Exception as e:b.emit({"type":"error","message":str(e)});sys.exit(1)
'''

class Runtime(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.root=Path(self.temp.name)
        self.env={**os.environ,"CAPY_SUPPORT":str(self.root/"support")}
        self.session={"id":"unique-test-session","sessionID":"session","provider":"Codex","root":str(self.root),"cwd":str(self.root)}

    def tearDown(self):self.temp.cleanup()

    def command(self,code):return [sys.executable,"-u","-c",BOOTSTRAP,str(BRIDGE),json.dumps(self.session),code]

    def run_fake(self,code):
        p=subprocess.run(self.command(code),env=self.env,text=True,capture_output=True,timeout=8)
        return p,[json.loads(x) for x in p.stdout.splitlines()]

    def test_codex_completed_turn_and_stdin(self):
        p,events=self.run_fake('import json,sys;assert sys.stdin.read()=="hello from camp";print(json.dumps({"type":"item.completed","item":{"type":"agent_message","text":"Hello"}}));print(json.dumps({"type":"turn.completed"}))')
        self.assertEqual([e["type"] for e in events],["started","text","done"])
        self.assertEqual(events[1]["text"],"Hello")

    def test_claude_stream_and_final_response(self):
        self.session["provider"]="Claude"
        code='import json;print(json.dumps({"type":"stream_event","event":{"delta":{"type":"text_delta","text":"Hello"}}}));print(json.dumps({"type":"result","result":"Hello world","is_error":False}))'
        _,events=self.run_fake(code)
        self.assertEqual(events[-2]["text"],"Hello world")
        self.assertEqual(events[-1]["type"],"done")

    def test_provider_error_is_not_reported_as_success(self):
        _,events=self.run_fake('import json;print(json.dumps({"type":"turn.failed","error":{"message":"session limit reached"}}))')
        self.assertEqual(events[-1]["type"],"error")
        self.assertIn("session limit",events[-1]["message"])

    def test_claude_auth_error_even_with_zero_exit(self):
        self.session["provider"]="Claude"
        _,events=self.run_fake('import json;print(json.dumps({"type":"result","result":"Not logged in","is_error":True}))')
        self.assertEqual(events[-1],{"type":"error","message":"Not logged in"})

    def test_missing_project_fails_before_start(self):
        self.session["cwd"]=str(self.root/"missing")
        _,events=self.run_fake('raise Exception("must not start")')
        self.assertEqual(len(events),1)
        self.assertIn("folder is missing",events[0]["message"])

    def test_empty_project_never_defaults_to_current_directory(self):
        self.session["cwd"]=""
        _,events=self.run_fake('raise Exception("must not start")')
        self.assertIn("folder is missing",events[0]["message"])

    def test_malformed_output_and_missing_completion_fail_closed(self):
        _,events=self.run_fake('print("not json");print("[]");print("{}")')
        self.assertEqual(events[-1]["type"],"error")

    def test_large_stderr_does_not_deadlock(self):
        _,events=self.run_fake('import sys,json;sys.stderr.write("x"*300000);sys.stderr.flush();print(json.dumps({"type":"turn.completed"}))')
        self.assertEqual(events[-1]["type"],"done")

    def test_duplicate_send_is_rejected_and_stop_kills_ignoring_child(self):
        code='import signal,json,time;signal.signal(signal.SIGTERM,signal.SIG_IGN);print(json.dumps({"type":"item.updated","item":{"type":"agent_message","text":"ready"}}),flush=True);time.sleep(60)'
        first=subprocess.Popen(self.command(code),env=self.env,text=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
        try:
            self.assertEqual(json.loads(first.stdout.readline())["type"],"started")
            self.assertEqual(json.loads(first.stdout.readline())["text"],"ready")
            _,second=self.run_fake('raise Exception("must not launch")')
            self.assertIn("already running",second[-1]["message"])
            start=time.monotonic();first.send_signal(signal.SIGTERM)
            output,_=first.communicate(timeout=7)
            self.assertLess(time.monotonic()-start,6)
            self.assertEqual(json.loads(output.splitlines()[-1])["type"],"cancelled")
            # The lock is released after completion; the next user send works.
            _,third=self.run_fake('import json;print(json.dumps({"type":"turn.completed"}))')
            self.assertEqual(third[-1]["type"],"done")
        finally:
            if first.poll() is None:first.kill();first.wait()

    def test_stop_also_kills_descendant_after_parent_exits(self):
        ready = str(self.root / "descendant-ready")
        child_code='import signal,time,pathlib;signal.signal(signal.SIGTERM,signal.SIG_IGN);pathlib.Path('+repr(ready)+').touch();time.sleep(60)'
        code='import subprocess,sys,json,time,pathlib;child=subprocess.Popen([sys.executable,"-u","-c",'+repr(child_code)+']);\nwhile not pathlib.Path('+repr(ready)+').exists():time.sleep(.01)\nprint(json.dumps({"type":"item.updated","item":{"type":"agent_message","text":"descendant ready"}}),flush=True);time.sleep(60)'
        first=subprocess.Popen(self.command(code),env=self.env,text=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
        try:
            first.stdout.readline()
            self.assertEqual(json.loads(first.stdout.readline())["text"],"descendant ready")
            first.send_signal(signal.SIGTERM)
            output,_=first.communicate(timeout=7)
            self.assertEqual(json.loads(output.splitlines()[-1])["type"],"cancelled")
        finally:
            if first.poll() is None:first.kill();first.wait()


    def test_claude_reports_fast_mode_fallback(self):
        self.session['provider']='Claude'
        self.session['_test_speed']='fast'
        code='import json;print(json.dumps({"type":"system","subtype":"init","fast_mode_state":"off"}));print(json.dumps({"type":"assistant","message":{"content":[{"type":"text","text":"Reply"}],"usage":{"speed":"standard"}}}));print(json.dumps({"type":"result","result":"Reply","is_error":False}))'
        _,events=self.run_fake(code)
        notices=[e['message'] for e in events if e['type']=='notice']
        self.assertTrue(any('Standard speed' in message for message in notices))
        self.assertEqual(events[-1]['type'],'done')

if __name__=="__main__":unittest.main()
