import os
import unittest
from unittest import mock

from spendlease import AdminClient, Lease


class LeaseTests(unittest.TestCase):
    def test_vendor_sdk_kwargs(self):
        lease = Lease("sll_test", "https://gateway.example/")
        self.assertEqual(
            lease.openai_kwargs(),
            {"base_url": "https://gateway.example/v1", "api_key": "sll_test"},
        )
        self.assertEqual(
            lease.anthropic_kwargs(),
            {"base_url": "https://gateway.example", "api_key": "sll_test"},
        )

    def test_rejects_principal_key(self):
        with self.assertRaisesRegex(ValueError, "sll_"):
            Lease("slk_principal")

    @mock.patch.dict(
        os.environ,
        {"SPENDLEASE_LEASE_TOKEN": "sll_env", "SPENDLEASE_URL": "https://lease.test"},
        clear=True,
    )
    def test_from_env(self):
        self.assertEqual(Lease.from_env().base_url, "https://lease.test")


class AdminClientTests(unittest.TestCase):
    @mock.patch("spendlease.client.request.urlopen")
    def test_set_mode_uses_guarded_form_endpoint(self, urlopen):
        response = mock.MagicMock()
        response.__enter__.return_value.read.return_value = b"table"
        urlopen.return_value = response

        body = AdminClient("https://gateway.example/", "admin-secret").set_mode(
            "prn_test", "enforce"
        )

        self.assertEqual(body, "table")
        req = urlopen.call_args.args[0]
        self.assertEqual(req.full_url, "https://gateway.example/admin/principals/prn_test/mode")
        self.assertEqual(req.data, b"mode=enforce")
        self.assertEqual(req.get_header("Authorization"), "Bearer admin-secret")
        self.assertEqual(req.get_header("X-spendlease-admin"), "1")

    def test_rejects_unknown_mode_without_request(self):
        with self.assertRaisesRegex(ValueError, "observe or enforce"):
            AdminClient().set_mode("prn_test", "disabled")


if __name__ == "__main__":
    unittest.main()
