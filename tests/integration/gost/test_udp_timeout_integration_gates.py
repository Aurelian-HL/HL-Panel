"""Offline gates for timeout focus selection and contract runner boundaries."""
import json
from pathlib import Path
import sys
import unittest

ROOT = Path(__file__).resolve().parent
sys.path.insert(0, str(ROOT))
import reliable_udp_pair as pair
import run_timeout_contracts as runner


class TimeoutPairGates(unittest.TestCase):
    variants = {'official', 'original', 'fixed', 'lifecycle', 'halfclose', 'drain', 'abort', 'dial', 'timeout'}

    def test_timeout_focus_is_dial_timeout_timeout_dial(self):
        self.assertEqual(pair.candidate_order(self.variants, timeout=True),
                         (('dial', 'timeout', 'timeout', 'dial'), 'timeout'))

    def test_timeout_focus_requires_complete_parent_chain_and_is_exclusive(self):
        for variants, flags in (
                (self.variants - {'dial'}, {'timeout': True}),
                (self.variants - {'timeout'}, {'timeout': True}),
                (self.variants, {'timeout': True, 'dial': True})):
            with self.subTest(variants=variants, flags=flags):
                with self.assertRaises(RuntimeError):
                    pair.candidate_order(variants, **flags)

    def test_timeout_runner_contract_inventory_is_stable(self):
        self.assertEqual(runner.PACKAGES, (
            'github.com/go-gost/x/listener/kcp', 'github.com/go-gost/x/dialer/kcp'))
        self.assertIn('TestKCPTimeoutEffectiveConfig', runner.expected_tests())
        self.assertIn('TestKCPTimeoutInvalidRejectedByInit/integer_seconds_overflow',
                      runner.expected_tests())


if __name__ == '__main__':
    unittest.main(verbosity=2)
