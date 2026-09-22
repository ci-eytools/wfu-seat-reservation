"""Scan-login-to-seat-selection client. Submission is deliberately unavailable."""
from dataclasses import dataclass, field
from datetime import datetime, timedelta
from html.parser import HTMLParser
from zoneinfo import ZoneInfo
from urllib.parse import urlencode
import hashlib
import json
import re
import time
from seat_guard import guard_session, BlockedSeatRequest
from seat_access import check_seat_page

TZ=ZoneInfo('Asia/Shanghai')
ORIGIN='https://office.chaoxing.com'

class _InputParser(HTMLParser):
    value=None
    def handle_starttag(self,tag,attrs):
        attrs=dict(attrs)
        if tag=='input' and attrs.get('id')=='submit_enc': self.value=attrs.get('value')

@dataclass
class Selection:
    day: str
    room_id: int
    seat_num: str
    start_time: str
    end_time: str
    room_name: str
    _payload: dict=field(repr=False)
    def summary(self):
        return {'state':'selected_not_submitted','day':self.day,'room_id':self.room_id,'room':self.room_name,
                'seat_num':self.seat_num,'start_time':self.start_time,'end_time':self.end_time,
                'signature_prepared':bool(self._payload.get('enc')),'submitted':False}
    def prepared_form(self):
        """Offline form encoding only. Never sends a request."""
        return urlencode(self._payload)

class SeatClient:
    def __init__(self,login,fid_enc='35bbd135397006a8',mapp_id='4109435'):
        self.login=login
        self.session=guard_session(login.session)
        self.fid_enc=fid_enc; self.mapp_id=mapp_id
        self.room_data=None; self.room_id=None; self.day=None
        self._submit_enc=None; self._server_now=None; self._clock_at=None
        self._rooms=[]; self._grid=[]

    def _json(self,method,path,params):
        kwargs={'params':params} if method=='GET' else {'data':params}
        response=self.session.request(method,ORIGIN+path,allow_redirects=False,timeout=30,**kwargs)
        if response.status_code!=200: raise RuntimeError(f'只读查询失败：{path} HTTP {response.status_code}')
        try: value=response.json()
        except ValueError: raise RuntimeError('接口未返回 JSON，可能登录已过期') from None
        if not value.get('success'): raise RuntimeError('接口拒绝查询：'+str(value.get('msg','未知原因')))
        return value.get('data')

    def open_home(self):
        url=ORIGIN+'/front/third/apps/seat/index?'+urlencode({'fidEnc':self.fid_enc,'mappId':self.mapp_id})
        result=check_seat_page(self.login,url)
        if result['state']!='seat_page_loaded': raise RuntimeError('未进入已登录座位页：'+str(result))
        html=self.login.seat_page_response.text
        stamp=re.search(r"serverNow\s*=\s*new Date\('([^']+)'\)",html)
        if not stamp: raise RuntimeError('未找到服务器时间')
        self._server_now=datetime.fromisoformat(stamp.group(1));self._clock_at=time.monotonic()
        self.day=self._server_now.date().isoformat()
        return {'state':'seat_home_loaded','server_day':self.day,'used_wfw_token':False}

    def server_now(self):
        if self._server_now is None: raise RuntimeError('请先打开首页')
        return self._server_now+timedelta(seconds=time.monotonic()-self._clock_at)

    def list_rooms(self,day=None):
        self.day=day or self.day
        if not self.day: raise RuntimeError('请先打开首页')
        datetime.strptime(self.day,'%Y-%m-%d')
        all_rooms=[]
        for page in range(1,21):
            data=self._json('GET','/data/apps/seat/room/list',{'time':'','cpage':page,'pageSize':100,
                'firstLevelName':'','secondLevelName':'','thirdLevelName':'','day':self.day,'deptIdEnc':self.fid_enc})
            all_rooms.extend(data['seatRoomList'])
            if page>=data.get('totalPage',1): break
        else: raise RuntimeError('房间分页超出预期')
        self._rooms=all_rooms
        return [{'id':r['id'],'name':self.room_name(r),'capacity':r['capacity'],
                 'selectable':r.get('status')==0 and r.get('isShow')==0 and r.get('isOpen')!=1} for r in all_rooms]

    @staticmethod
    def room_name(room):
        return ' - '.join(room[k] for k in ('firstLevelName','secondLevelName','thirdLevelName') if room.get(k))

    def open_room(self,room_id):
        self.room_data=None;self._submit_enc=None;self._grid=[];self.room_id=None
        if not any(r['id']==int(room_id) for r in self._rooms): raise ValueError('请从当前房间列表选择ID')
        window=self._json('GET','/data/apps/seat/room/reserve-window/check',{'roomId':room_id,'day':self.day,'deptIdEnc':self.fid_enc,'fidEnc':self.fid_enc})
        if window['status']!='AVAILABLE': raise ValueError('该日期预约窗口不可用：'+window['status'])
        r=self.session.get(ORIGIN+'/front/third/apps/seat/select',params={'deptIdEnc':self.fid_enc,'id':room_id,'day':self.day,'backLevel':2,'fidEnc':self.fid_enc},allow_redirects=False,timeout=30)
        if r.status_code!=200: raise RuntimeError('无法打开选座页')
        parser=_InputParser();parser.feed(r.text)
        if not parser.value: raise RuntimeError('选座页缺少 submit_enc，可能会话失效')
        data=self._json('POST','/data/apps/seat/room/info',{'id':room_id,'toDay':self.day,'fidEnc':self.fid_enc,'queryReserve':'true'})
        room=data['seatRoom']
        if room.get('status')!=0 or room.get('isOpen')==1 or room.get('isShow')==1: raise ValueError('房间当前不可在线预约')
        if not room.get('roleShow',True): raise ValueError('当前身份不能选择此房间')
        self.room_id=int(room_id);self.room_data=data;self._submit_enc=parser.value
        self._server_now=datetime.fromtimestamp(data['serverNow']/1000,TZ);self._clock_at=time.monotonic()
        if room.get('picSeatMode')==2:
            self._grid=self._json('GET','/data/apps/seat/seatgrid/roomid',{'roomId':room_id,'fidEnc':self.fid_enc})['seatDatas']
        elif room.get('picSeatMode')==0:
            raise NotImplementedError('当前版本支持网格/列表座位；自由布局需另外适配，未猜测座位数据')
        else:
            self._grid=[{'seatNum':str(n).zfill(3),'reserveStatus':0} for n in range(room['startSeatNum'],room['startSeatNum']+room['capacity'])]
        return {'room':self.room_name(room),'capacity':room['capacity'],'day':self.day,'selectable_time_slots':self.time_slots()}

    def time_slots(self):
        if not self.room_data: raise RuntimeError('请先打开房间')
        cfg=self.room_data['seatConfig'];room=self.room_data['seatRoom']
        if cfg.get('reserveMode')!=0: raise NotImplementedError('当前实现支持普通时段预约模式')
        if cfg.get('timeType')!=1: raise NotImplementedError('当前实现使用服务器配置时段；连续时间模式尚未适配')
        weekday=str(datetime.strptime(self.day,'%Y-%m-%d').isoweekday())
        raw=self.room_data.get('seatIntervalMap',{}).get(weekday,[])
        pause=json.loads(room.get('seatPauseDate',{}).get('dateJson','[]'))
        now=self.server_now();lead=max(0,cfg.get('allowReserveNow',0)) if cfg.get('allowReserveNow',0)>1 else 0
        slots=[]
        for entry in raw:
            start=datetime.fromisoformat(self.day+'T'+entry['startTime']).replace(tzinfo=TZ)
            end=datetime.fromisoformat(self.day+'T'+entry['endTime']).replace(tzinfo=TZ)
            if start<=now+timedelta(milliseconds=lead): continue
            if any(start<self._datetime(p['endDate']) and end>self._datetime(p['startDate']) for p in pause): continue
            slots.append({'startTime':entry['startTime'],'endTime':entry['endTime']})
        return slots

    @staticmethod
    def _datetime(value):
        if isinstance(value,(int,float)): return datetime.fromtimestamp(value/1000,TZ)
        d=datetime.fromisoformat(value)
        return d.replace(tzinfo=TZ) if d.tzinfo is None else d

    def _validate_period(self,start,end):
        slots=self.time_slots()
        cursor=start
        for slot in slots:
            if slot['startTime']==cursor:
                cursor=slot['endTime']
                if cursor==end: break
        if cursor!=end or start>=end: raise ValueError('所选时段已过期、不开放或跨越暂停时段')
        hours=(datetime.strptime(end,'%H:%M')-datetime.strptime(start,'%H:%M')).total_seconds()/3600
        cfg=self.room_data['seatConfig']
        if hours<cfg.get('minReserveDuration',0): raise ValueError('少于最低预约时长')
        if cfg.get('reserveDuration',0)>0 and hours>cfg['reserveDuration']: raise ValueError('超过最高预约时长')

    def available_seats(self,start,end):
        self._validate_period(start,end)
        data=self._json('POST','/data/apps/seat/getusedseatnums',{'roomId':self.room_id,'startTime':start,'endTime':end,'day':self.day,'fidEnc':self.fid_enc})
        occupied={str(r['seatNum']).zfill(3) for r in data['seatReserves']}
        disabled={str(a['seatNum']).zfill(3) for a in self.room_data.get('seatAttributes',[]) if a.get('isReserve')==0 or 5 in a.get('labelIds',[])}
        return sorted({str(s['seatNum']).zfill(3) for s in self._grid if s.get('reserveStatus',0)==0 and str(s['seatNum']).zfill(3) not in occupied|disabled})

    def choose(self,seat_num,start,end):
        seat_num=str(seat_num).zfill(3)
        if seat_num not in self.available_seats(start,end): raise ValueError('所选座位在该时段不可用')
        params={'deptIdEnc':self.fid_enc,'roomId':str(self.room_id),'day':self.day,'startTime':start,'endTime':end,'seatNum':seat_num,'captcha':'','wyToken':''}
        text=''.join(f'[{k}={params[k]}]' for k in sorted(params))+'['+self._submit_enc+']'
        params['enc']=hashlib.md5(text.encode()).hexdigest()
        self.selection=Selection(self.day,self.room_id,seat_num,start,end,self.room_name(self.room_data['seatRoom']),params)
        return self.selection

    def submit(self,*args,**kwargs):
        raise BlockedSeatRequest('本项目当前只允许选座和预览，未发送预约请求')