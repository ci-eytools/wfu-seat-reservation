import unittest,time
from unittest.mock import Mock
from types import SimpleNamespace
from datetime import datetime
import requests
from seat_guard import guard_session, BlockedSeatRequest
from seat_client import SeatClient,TZ

class GuardTests(unittest.TestCase):
    def test_mutations_block_before_network(self):
        for method,path in [('POST','submit'),('GET','submit'),('GET','cancel'),('GET','sign'),('POST','supervise'),('GET','%73ubmit')]:
            s=requests.Session(); network=Mock();s.send=network;guard_session(s)
            with self.assertRaises(BlockedSeatRequest): s.request(method,'https://office.chaoxing.com/data/apps/seat/'+path)
            network.assert_not_called()
    def test_unknown_office_endpoint_denied(self):
        s=requests.Session();network=Mock();s.send=network;guard_session(s)
        with self.assertRaises(BlockedSeatRequest): s.get('https://office.chaoxing.com/unknown/action')
        network.assert_not_called()
    def test_read_query_allowed(self):
        s=requests.Session();network=Mock(return_value=object());s.send=network;guard_session(s)
        s.post('https://office.chaoxing.com/data/apps/seat/getusedseatnums',data={'roomId':6299})
        self.assertEqual(network.call_count,1)
    def test_redirect_to_submit_is_blocked(self):
        s=guard_session(requests.Session())
        r=requests.Response();r.status_code=302;r.url='https://office.chaoxing.com/front/third/apps/seat/index'
        r.headers['Location']='https://office.chaoxing.com/data/apps/seat/submit';r._content=b'';r.request=requests.Request('GET',r.url).prepare()
        adapter=Mock();adapter.send.return_value=r;s.mount('https://',adapter)
        with self.assertRaises(BlockedSeatRequest):s.get(r.url)
        self.assertEqual(adapter.send.call_count,1)

class SelectionTests(unittest.TestCase):
    def setUp(self):
        self.c=SeatClient(SimpleNamespace(session=requests.Session()))
        self.c.day='2030-01-01';self.c.room_id=6299;self.c._submit_enc='fixture'
        self.c._server_now=datetime(2030,1,1,8,0,tzinfo=TZ);self.c._clock_at=time.monotonic()
        self.c.room_data={'seatConfig':{'reserveMode':0,'timeType':1,'minReserveDuration':0.5,'reserveDuration':2},
            'seatRoom':{'firstLevelName':'Fixture'},'seatIntervalMap':{'2':[{'startTime':'09:00','endTime':'09:30'},{'startTime':'10:00','endTime':'10:30'}]},
            'seatAttributes':[{'seatNum':'003','isReserve':0}]}
        self.c._grid=[{'seatNum':str(x).zfill(3),'reserveStatus':0} for x in (1,2,3)]
        self.c._json=Mock(return_value={'seatReserves':[{'seatNum':'002'}]})
    def test_occupied_disabled_excluded_and_preview_only(self):
        self.assertEqual(self.c.available_seats('09:00','09:30'),['001'])
        choice=self.c.choose('001','09:00','09:30')
        self.assertFalse(choice.summary()['submitted'])
        self.assertIn('startTime=09%3A00',choice.prepared_form())
        self.assertNotIn('fixture',repr(choice))
        with self.assertRaises(BlockedSeatRequest):self.c.submit()
    def test_gap_rejected_before_query(self):
        with self.assertRaises(ValueError):self.c.available_seats('09:00','10:30')
        self.c._json.assert_not_called()
    def test_occupied_cannot_be_selected(self):
        with self.assertRaises(ValueError):self.c.choose('002','09:00','09:30')

if __name__=='__main__': unittest.main()