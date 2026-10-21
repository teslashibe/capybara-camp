import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).parents[1]/'Resources'))
import activity
import bridge

class TuningTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        catalog = {'models': [
            {'slug':'sol','visibility':'list','default_reasoning_level':'low',
             'supported_reasoning_levels':[{'effort':'low'},{'effort':'high'},{'effort':'ultra'}],
             'service_tiers':[{'id':'priority'},{'id':'ultrafast'}]},
            {'slug':'luna','visibility':'list','supported_reasoning_levels':[{'effort':'low'},{'effort':'max'}],
             'additional_speed_tiers':['fast']}]}
        (self.root/'models_cache.json').write_text(json.dumps(catalog))
        self.s={'provider':'Codex','root':str(self.root),'sessionID':'test','model':'sol'}

    def test_codex_effort_and_fast_use_per_process_config(self):
        args=bridge.runtime_command(self.s,'sol','high','fast')
        self.assertIn('model_reasoning_effort="high"',args)
        self.assertIn('service_tier="fast"',args)
        self.assertIn('features.fast_mode=true',args)
        self.assertIn('--ignore-user-config',args)

    def test_codex_ultrafast_is_a_separate_speed(self):
        args=bridge.runtime_command(self.s,'sol','ultra','ultrafast')
        self.assertIn('service_tier="ultrafast"',args)
        self.assertIn('features.ultrafast_mode=true',args)
        self.assertIn('model_reasoning_effort="ultra"',args)
        with self.assertRaises(ValueError): bridge.runtime_command(self.s,'luna','low','ultrafast')

    def test_standard_explicitly_clears_fast(self):
        self.assertIn('service_tier="default"',bridge.runtime_command(self.s,'sol','','standard'))
        args=bridge.runtime_command({**self.s,'provider':'Claude'},'opus','','standard')
        self.assertEqual(json.loads(args[args.index('--settings')+1]),{'fastMode':False})

    def test_claude_effort_and_fast_are_session_scoped(self):
        args=bridge.runtime_command({**self.s,'provider':'Claude'},'opus','max','fast')
        self.assertEqual(args[args.index('--effort')+1],'max')
        self.assertEqual(json.loads(args[args.index('--settings')+1]),{'fastMode':True})
        self.assertIn('--safe-mode',args)
        self.assertEqual(args[args.index('--tools')+1],'')

    def test_claude_does_not_offer_or_accept_ultrafast(self):
        for model in ('opus','sonnet','haiku','fable'):
            self.assertNotIn('ultrafast',activity.claude_capabilities(model)['speeds'])
            with self.assertRaises(ValueError): bridge.runtime_command({**self.s,'provider':'Claude'},model,'','ultrafast')

    def test_claude_fast_only_on_supported_opus(self):
        for model in ('sonnet','haiku','fable','claude-opus-4-7'):
            with self.assertRaises(ValueError): bridge.runtime_command({**self.s,'provider':'Claude'},model,'','fast')
        self.assertIn('fast',activity.claude_capabilities('claude-opus-5-5')['speeds'])

    def test_unsupported_effort_is_rejected(self):
        with self.assertRaises(ValueError): bridge.runtime_command(self.s,'luna','ultra','standard')
        with self.assertRaises(ValueError): bridge.runtime_command({**self.s,'provider':'Claude'},'sonnet','ultra','standard')

    def test_inherited_effort_cannot_override_saved_choice(self):
        import os
        from unittest.mock import patch
        with patch.dict(os.environ,{'CLAUDE_CODE_EFFORT_LEVEL':'max','ANTHROPIC_MODEL':'wrong'}):
            env=bridge.runtime_env({**self.s,'provider':'Claude'})
        self.assertNotIn('CLAUDE_CODE_EFFORT_LEVEL',env)
        self.assertNotIn('ANTHROPIC_MODEL',env)
