import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parents[1] / 'Resources'))
import activity
import bridge

class ActivityTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.source = {'id': 'Claude:' + str(self.root), 'provider': 'Claude', 'root': str(self.root), 'label': 'work'}
        project = self.root / 'projects' / 'project'
        project.mkdir(parents=True)
        self.path = project / 'parent.jsonl'
        self.path.write_text(json.dumps({'type': 'assistant', 'message': {'model': 'claude-opus-5-5'}, 'sessionId': 'parent'})+'\n')
        self.parent = bridge.session(self.source, 'parent', 'Parent', str(self.root), 1, str(self.path))

    def workflow(self, ident='wf_current', complete=False):
        folder = self.path.with_suffix('') / 'subagents/workflows' / ident
        folder.mkdir(parents=True)
        rows = [{'type': 'started', 'agentId': 'a1', 'label': 'review', 'phase': 'Review'}]
        if complete:
            rows.append({'type': 'result', 'agentId': 'a1', 'result': 'done'})
        (folder / 'journal.jsonl').write_text('\n'.join(json.dumps(x) for x in rows)+'\n')
        (folder / 'agent-a1.jsonl').write_text(json.dumps({'type': 'assistant','isSidechain':True,'message':{'model':'claude-sonnet-5-5','content':[{'type':'text','text':'Reviewing'}]}})+'\n')
        return folder

    def test_live_parent_and_nested_workflow_are_grouped(self):
        self.workflow()
        with patch.object(activity, 'live_claude', return_value=[{'sessionId':'parent','status':'busy','name':'Live parent','pid':123,'cwd':str(self.root)}]):
            result = activity.collect([self.source])
        live = result['activity'][0]
        self.assertEqual(live['state'], 'running')
        self.assertEqual(live['children'][0]['name'], 'review')
        self.assertEqual(live['children'][0]['status'], 'running')
        self.assertEqual(live['children'][0]['model'], 'claude-sonnet-5-5')
        self.assertEqual(len(result['sessions']), 1)

    def test_idle_parent_does_not_make_old_incomplete_agent_run(self):
        self.workflow()
        self.assertEqual(activity.children_for(self.parent, False)[0]['status'], 'unknown')

    def test_completed_agent_stays_done_under_running_parent(self):
        self.workflow(complete=True)
        self.assertEqual(activity.children_for(self.parent, True)[0]['status'], 'done')

    def test_stale_registry_not_used_when_cli_reports_no_live_sessions(self):
        (self.root/'sessions').mkdir()
        (self.root/'sessions/123.json').write_text('{"status":"busy","sessionId":"parent","pid":123}')
        completed = type('Reply', (), {'returncode':0,'stdout':'[]'})()
        with patch.object(activity.subprocess, 'run', return_value=completed):
            self.assertEqual(activity.live_claude(self.source), [])

    def test_probe_failure_is_explicit(self):
        with patch.object(activity, 'live_claude', side_effect=ValueError('unavailable')):
            result = activity.collect([self.source])
        self.assertEqual(result['errors'][self.source['id']], 'unavailable')

    def test_external_owner_blocks_resume_even_when_idle(self):
        with patch.object(activity, 'live_claude', return_value=[{'sessionId':'parent','status':'idle'}]):
            with self.assertRaisesRegex(ValueError, 'open in Claude Code'):
                activity.assert_available(self.parent)

    def test_model_selection_reaches_both_cli_commands(self):
        for provider, model in [('Codex','gpt-6-luna'),('Claude','sonnet')]:
            command = bridge.runtime_command({**self.parent,'provider':provider},model)
            self.assertEqual(command[command.index('--model')+1],model)
        with self.assertRaises(ValueError): bridge.runtime_command(self.parent,'--bad')

    def test_subagent_transcript_is_readable_but_cannot_resume(self):
        folder=self.workflow()
        child={**self.parent,'path':str(folder/'agent-a1.jsonl'),'isSubagent':True}
        self.assertEqual(bridge.transcript(child)['messages'][0]['text'],'Reviewing')
        with self.assertRaisesRegex(ValueError,'read-only'): activity.assert_available(child)

    def test_model_catalog_uses_profile_cache_and_hides_internal_entries(self):
        source={**self.source,'provider':'Codex'}
        (self.root/'models_cache.json').write_text(json.dumps({'models':[{'slug':'allowed','visibility':'list'},{'slug':'private','visibility':'hide'}]}))
        self.assertEqual([d['id'] for d in activity.models_for(source)],['allowed'])

    def test_codex_descendants_use_native_edges_without_inventing_live_status(self):
        import sqlite3
        db = sqlite3.connect(self.root / 'state_5.sqlite')
        db.executescript("""
            CREATE TABLE threads(id,title,rollout_path,updated_at,model,agent_nickname,agent_role);
            CREATE TABLE thread_spawn_edges(parent_thread_id,child_thread_id,status);
            INSERT INTO threads VALUES('child','Review','',1,'gpt-6-luna','Reviewer','review');
            INSERT INTO threads VALUES('grandchild','Check','',2,'gpt-6-sol','Checker','test');
            INSERT INTO thread_spawn_edges VALUES('parent','child','open');
            INSERT INTO thread_spawn_edges VALUES('child','grandchild','open');
        """)
        db.close()
        children=activity.children_for({**self.parent,'provider':'Codex'},True)
        self.assertEqual({x['id'] for x in children},{'child','grandchild'})
        self.assertTrue(all(x['status']=='unknown' for x in children))
        self.assertEqual(children[0]['model'],'gpt-6-luna')
