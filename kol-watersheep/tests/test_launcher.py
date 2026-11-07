"""Offline tests; no KoL action, network, Docker, or model inference."""
from importlib.machinery import SourceFileLoader
import ipaddress
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch, call

main = SourceFileLoader("kolstack", str(Path(__file__).resolve().parents[1] / "assets" / "kolstack")).load_module()
compat = SourceFileLoader("compat_adapter", str(Path(__file__).resolve().parents[1] / "assets" / "compat_adapter.py")).load_module()


class TestLauncher(unittest.TestCase):
    def test_owned_without_pid(self):
        self.assertFalse(main.owned_alive({}))
        self.assertFalse(main.owned_alive({"pid": -1, "identity": "x"}))

    def test_default_paths_point_to_local_koL_clone(self):
        c = main.load_config()
        self.assertEqual(Path(c["WS_KOL"]).name, "kol-watersheep")
        self.assertEqual(c["WS_USE_CONDA"], "1")

    def test_adapter_candidates_match_existing_git_layout(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            adapter = root / "data/git/donCannoli-burns-don_combo-release/scripts/don_combo/watersheep_post_adapter.py"
            adapter.parent.mkdir(parents=True)
            adapter.write_text("# fake file, not executed")
            self.assertEqual(main.published_adapter_path({"XPRA_ROOT": td}), adapter)
            self.assertEqual(main.adapter_path({"XPRA_ROOT": td}), main.ROOT / "compat_adapter.py")

    def test_xpra_web_port_from_docker_publish_8080(self):
        from types import SimpleNamespace
        with patch.object(main, "compose", return_value=SimpleNamespace(stdout="127.0.0.1:8080\n")) as cmd:
            self.assertEqual(main.xpra_web_port({"XPRA_ROOT": "/unused"}), 8080)
        cmd.assert_called_once_with({"XPRA_ROOT": "/unused"}, "port", "kolmafia", "8080", capture=True)

    def test_xpra_web_port_from_alternate_publish(self):
        from types import SimpleNamespace
        with patch.object(main, "compose", return_value=SimpleNamespace(stdout="0.0.0.0:18080\n")):
            self.assertEqual(main.xpra_web_port({"XPRA_ROOT": "/unused"}), 18080)

    def test_xpra_web_port_rejects_missing_mapping(self):
        from types import SimpleNamespace
        with patch.object(main, "compose", return_value=SimpleNamespace(stdout="")):
            with self.assertRaises(RuntimeError):
                main.xpra_web_port({"XPRA_ROOT": "/unused"})

    def test_gateway_private_rfc1918(self):
        self.assertTrue(ipaddress.IPv4Address("172.29.0.1").is_private)

    def test_dual_request_contract(self):
        from urllib.parse import urlencode
        import json
        obj = {"state": {"mode": "shadow-only; no action authorized"},
               "questions": {"tile_class": {"type": "choice",
                   "criteria": {letter: letter for letter in "ABCDEF"}}}}
        query = urlencode({"request": json.dumps(obj)})
        self.assertEqual(compat.request_from_query(query), obj)
        obj["state"]["mode"] = "action authorized"
        with self.assertRaises(ValueError):
            compat.request_from_query(urlencode({"request": json.dumps(obj)}))
        with self.assertRaises(ValueError):
            compat.request_from_query(query + "&extra=hello")

    def test_private_binding_guard(self):
        self.assertEqual(compat.private_ip("172.29.0.1"), "172.29.0.1")
        with self.assertRaises(ValueError):
            compat.private_ip("0.0.0.0")
        with self.assertRaises(ValueError):
            compat.private_ip("8.8.8.8")

    def test_cpu_install_precedes_editable_and_verifies_backend(self):
        c = {"WS_TORCH_BACKEND": "cpu", "WS_KOL": "/repo/kol-watersheep"}
        with patch.object(main, "torch_cpu_ready", side_effect=[False, True, True]) as ready, \
                patch.object(main, "run") as runner:
            main.install_watersheep(c, "/env/bin/python")
        cmds = [x.args[0] for x in runner.call_args_list]
        self.assertEqual(len(cmds), 2)
        self.assertIn("--force-reinstall", cmds[0])
        self.assertIn("https://download.pytorch.org/whl/cpu", cmds[0])
        self.assertEqual(cmds[1][-2:], ["-e", "/repo/kol-watersheep"])
        self.assertEqual(ready.call_count, 3)

    def test_existing_cpu_torch_skips_redownload(self):
        c = {"WS_TORCH_BACKEND": "cpu", "WS_KOL": "/repo/kol-watersheep"}
        with patch.object(main, "torch_cpu_ready", side_effect=[True, True]), \
                patch.object(main, "run") as runner:
            main.install_watersheep(c, "/env/bin/python")
        self.assertEqual(runner.call_count, 1)
        self.assertEqual(runner.call_args.args[0][-2:], ["-e", "/repo/kol-watersheep"])

    def test_existing_backend_is_explicit_escape_hatch(self):
        c = {"WS_TORCH_BACKEND": "existing", "WS_KOL": "/repo/kol-watersheep"}
        with patch.object(main, "run") as runner:
            main.install_watersheep(c, "/env/bin/python")
        self.assertEqual(runner.call_count, 1)
        self.assertNotIn("--index-url", runner.call_args.args[0])

    def test_invalid_backend_fails_before_pip(self):
        c = {"WS_TORCH_BACKEND": "cu130", "WS_KOL": "/repo/kol-watersheep"}
        with patch.object(main, "run") as runner:
            with self.assertRaises(RuntimeError):
                main.install_watersheep(c, "/env/bin/python")
        runner.assert_not_called()

    def test_startticks(self):
        self.assertIsNone(main.start_ticks(-1))


if __name__ == "__main__":
    unittest.main()
