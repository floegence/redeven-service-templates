const {test}=require('node:test');
const assert=require('node:assert/strict');
const {parseStartupURL}=require('./index.cjs');
test('startup URL is the first complete URL after the exact prefix',()=>{
 const source={line_prefix:'dsh web: '};
 const target='http://127.0.0.1:3080/start?token=private#page';
 assert.equal(parseStartupURL('initializing\ndsh web: '+target+' (LAN: http://172.17.0.1/)\ndsh web: http://127.0.0.1:3080/later',source),target);
 for(const text of ['','not dsh web: '+target,'dsh web: file:///etc/passwd','dsh web: http://user:secret@localhost/','x'.repeat(65536)])assert.throws(()=>parseStartupURL(text,source));
});
