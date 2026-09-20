"""Offline fixtures for manager-compatible proxy reachability and IP checks."""
import json
import unittest
from types import SimpleNamespace
from unittest.mock import patch

import protocol_proxy_probe as probe


def response(status=200, ip="203.0.113.1"):
    return SimpleNamespace(status_code=status, text=f"ip={ip}\nloc=US\ncolo=LAX\n")


class ProxyProbeTests(unittest.TestCase):
    proxy = "socks5h://user:secret_country-us_session-12345678@geo.iproyal.com:12321"

    def run_probe(self, replies, proxy_url=None):
        with patch.object(probe.requests, "get", side_effect=replies) as request, patch.object(probe.time, "sleep") as sleep:
            result = probe.probe_proxy(proxy_url or self.proxy)
        return result, request, sleep

    def test_consistent_ip_uses_two_independent_requests_with_same_proxy(self):
        result, request, sleep = self.run_probe([response(302), response(), response()])
        self.assertTrue(result["ok"])
        self.assertEqual(result["ip_check_status"], "consistent")
        self.assertEqual(result["egress_first"]["ip"], result["egress_confirm"]["ip"])
        self.assertEqual(request.call_count, 3)
        sleep.assert_called_once_with(0.8)
        for call in request.call_args_list:
            self.assertEqual(call.kwargs["proxies"], {"http": self.proxy, "https": self.proxy})
            self.assertEqual(call.kwargs["impersonate"], "chrome131")
        self.assertFalse(request.call_args_list[0].kwargs["allow_redirects"])
        self.assertEqual([call.kwargs["timeout"] for call in request.call_args_list], [20, 12, 12])
        self.assertNotIn("secret", json.dumps(result))

    def test_different_ip_rejects_before_login(self):
        result, request, sleep = self.run_probe([response(302), response(ip="203.0.113.1"), response(ip="203.0.113.2")])
        self.assertFalse(result["ok"])
        self.assertTrue(result["retryable"])
        self.assertEqual(result["error_code"], "proxy_ip_unstable")
        self.assertEqual(result["ip_check_status"], "unstable")
        self.assertIn("203.0.113.1 -> 203.0.113.2", result["error"])

    def test_root_403_is_allowed_like_manager(self):
        result, _, _ = self.run_probe([response(403), response(), response()])
        self.assertTrue(result["ok"])
        self.assertEqual(result["auth_status"], 403)

    def test_root_failures_do_not_run_ip_checks(self):
        for status in (0, 429, 500, 502, 503):
            with self.subTest(status=status):
                result, request, sleep = self.run_probe([response(status)])
                self.assertFalse(result["ok"])
                self.assertEqual(request.call_count, 1)
                sleep.assert_not_called()

    def test_root_network_failure_is_retryable(self):
        result, request, _ = self.run_probe([RuntimeError("Could not resolve proxy")])
        self.assertFalse(result["ok"])
        self.assertTrue(result["retryable"])
        self.assertEqual(result["error_code"], "proxy_dns_failed")

    def test_trace_failure_is_diagnostic_only_like_manager(self):
        for replies in ([response(302), RuntimeError("trace timeout")], [response(302), response(), RuntimeError("trace timeout")]):
            with self.subTest(replies=len(replies)):
                result, _, _ = self.run_probe(replies)
                self.assertTrue(result["ok"])
                self.assertEqual(result["ip_check_status"], "unavailable")
                self.assertEqual(result["trace_error"], "trace timeout")

    def test_missing_ip_is_not_reported_as_consistent(self):
        for first, second in (("", "203.0.113.1"), ("203.0.113.1", ""), ("", "")):
            with self.subTest(first=first, second=second):
                result, _, _ = self.run_probe([response(302), response(ip=first), response(ip=second)])
                self.assertTrue(result["ok"])
                self.assertEqual(result["ip_check_status"], "unavailable")

    def test_trace_error_does_not_expose_proxy_credentials(self):
        result, _, _ = self.run_probe([response(302), RuntimeError(f"failed: {self.proxy}, password=secret_country-us_session-12345678")])
        self.assertTrue(result["ok"])
        self.assertNotIn("secret", json.dumps(result))

    def test_socks5_uses_remote_dns_like_oauth(self):
        result, request, _ = self.run_probe([response(302), response(), response()], self.proxy.replace("socks5h://", "socks5://"))
        self.assertTrue(result["ok"])
        for call in request.call_args_list:
            self.assertEqual(call.kwargs["proxies"]["https"], self.proxy)

    def test_empty_proxy_is_rejected_without_network(self):
        with patch.object(probe.requests, "get") as request:
            result = probe.probe_proxy("")
        self.assertFalse(result["ok"])
        self.assertFalse(result["retryable"])
        request.assert_not_called()


if __name__ == "__main__":
    unittest.main()
