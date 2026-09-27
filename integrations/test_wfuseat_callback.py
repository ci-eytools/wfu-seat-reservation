import json
import unittest
from wfuseat_callback import forward_wfuseat_callback

class Fake:
    def __init__(self): self.calls=[]
    def open(self, req, timeout):
        self.calls.append(req)
        self.status=200
        return self
    def __enter__(self): return self
    def __exit__(self,*args): pass

class RelayTest(unittest.TestCase):
    def test_only_seat_buttons(self):
        f=Fake()
        self.assertFalse(forward_wfuseat_callback({"callback_query":{"data":"quiz:next"}},"dummy",opener=f))
        self.assertFalse(forward_wfuseat_callback({"message":{"text":"/start"}},"dummy",opener=f))
        self.assertEqual(f.calls,[])
    def test_forwards_minimal_payload(self):
        f=Fake()
        update={"callback_query":{"id":"x","data":"wfuseat:abc:0","from":{"id":9},"message":{"message_id":7,"chat":{"id":-12},"text":"private unrelated content"}}}
        self.assertTrue(forward_wfuseat_callback(update,"dummy",opener=f))
        body=json.loads(f.calls[0].data)
        self.assertEqual(body["message"],{"message_id":7,"chat":{"id":-12}})
        self.assertNotIn("from",body)
        self.assertEqual(len(f.calls[0].get_header("Authorization")),71)
    def test_rejects_plaintext_remote(self):
        f=Fake()
        self.assertTrue(forward_wfuseat_callback({"callback_query":{"data":"wfuseat:x:0"}},"dummy","http://example.com",opener=f))
        self.assertEqual(f.calls,[])

if __name__=="__main__": unittest.main()
