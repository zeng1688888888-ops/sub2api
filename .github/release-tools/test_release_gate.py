import unittest
from pathlib import Path
import yaml

ROOT = Path(__file__).resolve().parents[2]

def workflow(name):
    return yaml.load((ROOT / '.github/workflows' / name).read_text(), Loader=yaml.BaseLoader)

class ReleaseGateTest(unittest.TestCase):
    def test_release_requires_both_validation_jobs(self):
        jobs = workflow('release.yml')['jobs']
        for job in ('build-frontend', 'release'):
            self.assertIn('validate-ci', jobs[job]['needs'])
            self.assertIn('validate-security', jobs[job]['needs'])
            self.assertNotIn('always()', jobs[job].get('if', ''))
        for name, file in (('validate-ci', 'backend-ci.yml'), ('validate-security', 'security-scan.yml')):
            job = jobs[name]
            self.assertEqual(job['uses'], './.github/workflows/' + file)
            self.assertEqual(job['with']['source_ref'], '${{ needs.prepare.outputs.sha }}')
            self.assertNotIn('continue-on-error', job)

    def test_reusable_checks_checkout_requested_source(self):
        for name in ('backend-ci.yml', 'security-scan.yml'):
            data = workflow(name)
            self.assertIn('workflow_call', data['on'])
            self.assertIn('workflow_dispatch', data['on'])
            for job in data['jobs'].values():
                checkout = [s for s in job['steps'] if s.get('uses', '').startswith('actions/checkout@')]
                self.assertEqual(len(checkout), 1)
                self.assertEqual(checkout[0]['with']['ref'], '${{ inputs.source_ref || github.sha }}')

    def test_immutable_go_version_has_one_source(self):
        version = next(line.split()[1] for line in (ROOT/'backend/go.mod').read_text().splitlines() if line.startswith('go '))
        for name in ('Dockerfile', 'backend/Dockerfile', 'deploy/Dockerfile'):
            self.assertIn('golang:' + version + '-alpine', (ROOT/name).read_text())
        for name in ('backend-ci.yml', 'security-scan.yml', 'release.yml'):
            self.assertNotIn("grep -q 'go1.", (ROOT/'.github/workflows'/name).read_text())

if __name__ == '__main__':
    unittest.main()
