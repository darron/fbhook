import unittest
import update_homebrew as brew


class DeliveryTests(unittest.TestCase):
    def test_formula_uses_verified_platform_hashes(self):
        hashes = {f'fbhook_1.2.3_{system}_{arch}.tar.gz': 'a' * 64
                  for system in ('darwin', 'linux') for arch in ('amd64', 'arm64')}
        formula = brew.formula('darron/fbhook', 'v1.2.3', hashes)
        self.assertIn('class Fbhook < Formula', formula)
        self.assertIn('bin.install "fbhook"', formula)
        self.assertEqual(formula.count('sha256 "' + 'a' * 64 + '"'), 4)
        with self.assertRaises(ValueError):
            brew.formula('darron/fbhook', 'v1.2.3', {})


if __name__ == '__main__':
    unittest.main()
