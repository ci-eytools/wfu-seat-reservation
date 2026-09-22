"""Read-only seat transport policy: submission cannot reach the network."""
from urllib.parse import urlsplit, unquote
import posixpath

READ_APIS = {
    ('GET','/data/apps/seat/config'),
    ('GET','/data/apps/seat/index'),
    ('GET','/data/apps/seat/room/list'),
    ('GET','/data/apps/seat/room/reserve-window/check'),
    ('POST','/data/apps/seat/room/info'),
    ('POST','/data/apps/seat/room/info/switch'),
    ('POST','/data/apps/seat/getusedtimes'),
    ('POST','/data/apps/seat/getusedseatnums'),
    ('GET','/data/apps/seat/getdrawseat'),
    ('GET','/data/apps/seat/seatgrid/roomid'),
}
class BlockedSeatRequest(RuntimeError): pass

def check_request(method,url):
    p=urlsplit(url)
    path=p.path
    for _ in range(3): path=unquote(path)
    path=posixpath.normpath(path).lower()
    if '/data/apps/seat' in path:
        if p.hostname!='office.chaoxing.com' or (method.upper(),path) not in READ_APIS:
            raise BlockedSeatRequest('已在发送前拦截非只读座位接口')
    if p.hostname=='office.chaoxing.com':
        pages={'/front/third/apps/seat/index','/front/third/apps/seat/list','/front/third/apps/seat/select'}
        if (method.upper(),path) not in READ_APIS and not (method.upper()=='GET' and path in pages):
            raise BlockedSeatRequest('已拦截未确认的 office 接口')

def guard_session(session):
    if getattr(session,'_seat_guard',False): return session
    original_send=session.send
    session.blocked_seat_requests=0
    def guarded_send(request,**kwargs):
        try: check_request(request.method,request.url)
        except BlockedSeatRequest:
            session.blocked_seat_requests+=1
            raise
        return original_send(request,**kwargs)
    session.send=guarded_send
    session._seat_guard=True
    return session