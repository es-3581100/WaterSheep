"""Actual localhost GET+POST adapter round trip to fake Go /v1/systemone."""
from importlib.machinery import SourceFileLoader
from pathlib import Path
import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlencode
from urllib.request import Request, urlopen
from urllib.error import HTTPError

compat = SourceFileLoader("dual_adapter_test", str(Path(__file__).resolve().parents[1] / "assets" / "compat_adapter.py")).load_module()


class FakeGo(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass
    def do_POST(self):
        if self.path != '/v1/systemone':
            self.send_error(404)
            return
        data = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))))
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        result = {"model": "fake-go-transport", "answers": {"tile_class": {
            "type": "choice", "choice": "A", "confidence": 1.0,
            "probabilities": {k: (1.0 if k == 'A' else 0.0) for k in 'ABCDEF'}}},
            "usage": {"input_tokens": 3, "output_tokens": 0}}
        body = json.dumps(result).encode()
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class HTTPBridgeTests(unittest.TestCase):
    def test_both_transports_and_nonshadow_denial(self):
        upstream = ThreadingHTTPServer(("127.0.0.1", 0), FakeGo)
        adapter = ThreadingHTTPServer(("127.0.0.1", 0), compat.handler(upstream.server_address[1]))
        t1 = threading.Thread(target=upstream.serve_forever)
        t2 = threading.Thread(target=adapter.serve_forever)
        t1.start(); t2.start()
        try:
            url = f"http://127.0.0.1:{adapter.server_address[1]}"
            data = {"state": {"mode": "shadow-only; no action authorized"},
                    "questions": {"tile_class": {"type": "choice", "criteria": {k: k for k in "ABCDEF"}}}}
            query = urlencode({"request": json.dumps(data)})
            with urlopen(url + '/v1/kolmafia?' + query) as response:
                self.assertEqual(json.load(response)['model'], 'fake-go-transport')
            req = Request(url + '/v1/kolmafia', data=query.encode(), method='POST',
                          headers={'Content-Type':'application/x-www-form-urlencoded'})
            with urlopen(req) as response:
                self.assertEqual(json.load(response)['answers']['tile_class']['choice'], 'A')
            data['state']['mode'] = 'live-action'
            try:
                urlopen(url + '/v1/kolmafia?' + urlencode({'request':json.dumps(data)}))
                self.fail('must reject non-shadow')
            except HTTPError as e:
                self.assertEqual(e.code, 400)
            with urlopen(url + '/health') as response:
                self.assertEqual(json.load(response)['watersheep_tcp'], 'listening')
        finally:
            adapter.shutdown(); upstream.shutdown()
            adapter.server_close(); upstream.server_close()
            t1.join(); t2.join()

if __name__ == '__main__':
    unittest.main()
