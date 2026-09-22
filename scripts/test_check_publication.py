import unittest
from check_publication import inspect

class PublicationTests(unittest.TestCase):
    def test_high_confidence_secret_and_notebook_output(self):
        token = ("gh" + "p_" + "A" * 36).encode()
        self.assertTrue(inspect("example.txt", token))
        raw = b'{"cells":[{"outputs":[{"text":"hidden"}],"execution_count":1}]}'
        self.assertTrue(inspect("example.ipynb", raw))

    def test_runtime_file_rejected_and_clean_notebook_allowed(self):
        self.assertTrue(inspect("work/session.json", b"{}"))
        self.assertFalse(inspect("example.ipynb", b'{"cells":[{"outputs":[],"execution_count":null}]}'))
        self.assertFalse(inspect("main.go", b'package main'))

    def test_report_contains_no_secret(self):
        secret = ("gh" + "p_" + "A" * 36).encode()
        self.assertNotIn(secret.decode(), str(inspect("x", secret)))

if __name__ == "__main__":
    unittest.main()
