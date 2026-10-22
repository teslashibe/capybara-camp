"""Provider-owned permission settings; Camp never substitutes an approval decision."""
import json
import re
import subprocess


FIELDS = {'profile', 'sandbox', 'approval', 'reviewer', 'claudeMode'}


def normalize(provider, settings=None):
    settings = {} if settings is None else settings
    if not isinstance(settings, dict) or set(settings) - FIELDS:
        raise ValueError('Invalid permission settings')
    if any(not isinstance(v, str) or len(v.encode()) > 160 for v in settings.values()):
        raise ValueError('Invalid permission setting value')
    value = {k: v for k, v in settings.items() if v}
    if provider == 'Codex':
        if value.get('claudeMode'):
            raise ValueError('Claude permission modes cannot be used with Codex')
        if value.get('sandbox') and value.get('profile'):
            raise ValueError('Choose a Codex permission profile or sandbox, not both')
        if value.get('sandbox', '') not in ('', 'read-only', 'workspace-write', 'danger-full-access'):
            raise ValueError('Unsupported Codex sandbox')
        if value.get('approval', '') not in ('', 'on-request', 'never'):
            raise ValueError('Unsupported Codex approval policy')
        if value.get('reviewer', '') not in ('', 'user', 'auto_review'):
            raise ValueError('Unsupported Codex approval reviewer')
        if value.get('profile') and not re.fullmatch(r'[:a-zA-Z0-9_.-]+', value['profile']):
            raise ValueError('Invalid Codex permission profile')
    elif provider == 'Claude':
        if set(value) - {'claudeMode'} or value.get('claudeMode', '') not in ('', 'manual', 'acceptEdits', 'plan', 'auto', 'dontAsk', 'bypassPermissions'):
            raise ValueError('Unsupported Claude permission mode')
    else:
        raise ValueError('Choose a supported provider')
    return value


def codex_overrides(settings):
    value = normalize('Codex', settings)
    return {field: value[key] for key, field in [('profile', 'permissions'), ('sandbox', 'sandbox'),
            ('approval', 'approvalPolicy'), ('reviewer', 'approvalsReviewer')] if key in value}


def check_codex(settings, config, requirements):
    value = normalize('Codex', settings)
    choices = [('approval', 'allowedApprovalPolicies'), ('reviewer', 'allowedApprovalsReviewers'),
               ('sandbox', 'allowedSandboxModes')]
    for key, rule in choices:
        if key in value and requirements.get(rule) is not None and value[key] not in requirements[rule]:
            raise ValueError('This Codex permission choice is blocked by managed requirements')
    allowed_profiles = requirements.get('allowedPermissionProfiles')
    if value.get('sandbox') and allowed_profiles is not None:
        raise ValueError('Managed Codex requires a named permission profile')
    if value.get('profile'):
        name = value['profile']
        if name not in (':read-only', ':workspace', ':danger-full-access') and name not in (config.get('permissions') or {}):
            raise ValueError('This Codex permission profile is no longer installed')
        if allowed_profiles is not None and allowed_profiles.get(name) is not True:
            raise ValueError('This Codex permission profile is blocked by managed requirements')
    # Reject a contradictory explicit configuration instead of silently changing it.
    if value.get('reviewer') == 'auto_review' and value.get('approval', config.get('approval_policy')) == 'never':
        raise ValueError('Automatic review requires an interactive approval policy')
    return value


def sandbox_mode(response):
    return {'readOnly': 'read-only', 'workspaceWrite': 'workspace-write',
            'dangerFullAccess': 'danger-full-access'}.get((response.get('sandbox') or {}).get('type'))


def codex_summary(response):
    profile = response.get('activePermissionProfile') or {}
    sandbox = response.get('sandbox') or {}
    access = profile.get('id') or sandbox_mode(response) or sandbox.get('type') or 'Provider default access'
    policy = response.get('approvalPolicy')
    policy = policy if isinstance(policy, str) else 'Provider approval rules'
    reviewer = response.get('approvalsReviewer', 'Provider default reviewer')
    return ' · '.join(str(v) for v in (access, policy, reviewer))


def catalog(session):
    """Read settings, never launch a thread or load credential bodies into the UI."""
    from bridge import runtime_env
    from conversation import NativeConnection
    env = runtime_env(session)
    if session['provider'] == 'Claude':
        help_text = subprocess.run(['claude', '--help'], cwd=session['cwd'], env=env, capture_output=True,
                                   text=True, timeout=10, check=True).stdout
        match = re.search(r'--permission-mode\b[\s\S]*?\(choices:\s*([^)]*)\)', help_text)
        modes = re.findall(r'"([a-zA-Z]+)"', match.group(1)) if match else []
        if not modes:
            raise ValueError('The installed Claude CLI does not advertise permission modes')
        return {'provider': 'Claude', 'modes': modes, 'profiles': [], 'sandboxes': [],
                'approvals': [], 'reviewers': [], 'inherited': 'Claude profile and project rules',
                'note': 'Auto mode and bypass availability are checked by Claude for this account and model'}
    c = NativeConnection(['codex', 'app-server', '--stdio'], session['cwd'], env)
    try:
        c.rpc('initialize', {'clientInfo': {'name': 'capybara_permissions', 'version': '0.0.1'},
                             'capabilities': {'experimentalApi': True}})
        c.write({'method': 'initialized', 'params': {}})
        config = c.rpc('config/read', {'includeLayers': False, 'cwd': session['cwd']}).get('config') or {}
        requirements = c.rpc('configRequirements/read', {}).get('requirements') or {}
        profiles = [':read-only', ':workspace', ':danger-full-access'] + sorted((config.get('permissions') or {}).keys())
        allowed = requirements.get('allowedPermissionProfiles')
        if allowed is not None:
            profiles = [p for p in profiles if allowed.get(p) is True]
        inherited = [config.get('default_permissions') or config.get('sandbox_mode') or 'Provider default access',
                     config.get('approval_policy') or 'Provider approval rules', config.get('approvals_reviewer') or 'user']
        return {'provider': 'Codex', 'modes': [], 'profiles': list(dict.fromkeys(profiles)),
                'sandboxes': [] if allowed is not None else requirements.get('allowedSandboxModes') if requirements.get('allowedSandboxModes') is not None else ['read-only', 'workspace-write', 'danger-full-access'],
                'approvals': [v for v in ['on-request', 'never'] if requirements.get('allowedApprovalPolicies') is None or v in requirements['allowedApprovalPolicies']],
                'reviewers': [v for v in ['user', 'auto_review'] if requirements.get('allowedApprovalsReviewers') is None or v in requirements['allowedApprovalsReviewers']],
                'inherited': ' · '.join(v if isinstance(v, str) else 'Custom rules' for v in inherited),
                'note': 'Managed requirements remain authoritative at launch'}
    finally:
        c.close()
