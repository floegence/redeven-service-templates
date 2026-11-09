const {test}=require('node:test');
const assert=require('node:assert/strict');
const {gzipSync,gunzipSync}=require('node:zlib');
const {capturePinnedSource,sourceDigest}=require('./index.cjs');
const commit='a'.repeat(40), root='templates-'+commit;
const source={repository:'owner/templates',ref:'main',path:'templates/app'};
const files=[{path:'template.json',mode:'100644',content:Buffer.from('{}').toString('base64')},{path:'scripts/start.sh',mode:'100755',content:Buffer.from('exec server\n').toString('base64')}];
const expected={repository_id:123,commit_sha:commit,sha256:sourceDigest(files)};
function entry(name,data=Buffer.alloc(0),type='0',mode=0o644){
 const h=Buffer.alloc(512);h.write(name);h.write(mode.toString(8).padStart(7,'0')+'\0',100);h.write(data.length.toString(8).padStart(11,'0')+'\0',124);h.fill(32,148,156);h.write(type,156);h.write('ustar\0',257);h.write('00',263);h.write([...h].reduce((a,b)=>a+b,0).toString(8).padStart(6,'0')+'\0 ',148);
 return Buffer.concat([h,data,Buffer.alloc((512-data.length%512)%512)]);
}
function archive(changes={}){
 const record='comment='+commit+'\n';let length=record.length+3;while(String(length).length+1+record.length!==length)length=String(length).length+1+record.length;
 return gzipSync(Buffer.concat([entry('pax_global_header',Buffer.from(length+' '+record),'g'),entry(root+'/',Buffer.alloc(0),'5',0o775),...files.map(f=>entry(root+'/'+source.path+'/'+f.path,Buffer.from(f.content,'base64'),'0',f.mode==='100755'?0o775:0o664)),...(changes.extra??[]),Buffer.alloc(1024)]));
}
test('anonymous pinned capture uses one bounded public archive despite exhausted API quota',async()=>{
 const calls=[];
 const snapshot=await capturePinnedSource(source,expected,'',{fetch:async(url,options)=>{calls.push(url);assert.equal(options.headers.Authorization,undefined);assert.equal(options.redirect,'error');if(new URL(url).hostname==='api.github.com')return new Response('{}',{status:403,headers:{'X-RateLimit-Remaining':'0'}});assert.equal(url,'https://codeload.github.com/owner/templates/tar.gz/'+commit);return new Response(archive());}});
 assert.equal(calls.length,1);assert.deepEqual(snapshot.files,files.slice().sort((a,b)=>a.path.localeCompare(b.path)));assert.equal(snapshot.sha256,expected.sha256);assert.equal(snapshot.source.ref,'main');assert.equal(snapshot.source.repository_id,123);
});
test('pinned archives reject altered content and unsafe file kinds',async()=>{
 for(const [extra,code] of [
  [[entry(root+'/'+source.path+'/bad',Buffer.alloc(0),'2')],'TEMPLATE_SOURCE_PATH_INVALID'],
  [[entry(root+'/'+source.path+'/template.json',Buffer.from('changed'))],'TEMPLATE_SOURCE_PATH_CONFLICT'],
  [[entry(root+'/'+source.path+'/extra',Buffer.from('changed'))],'TEMPLATE_SOURCE_EXPECTATION_MISMATCH'],
  [[entry(root+'/'+source.path+'/../escape',Buffer.from('changed'))],'TEMPLATE_SOURCE_PATH_INVALID'],
 ])await assert.rejects(capturePinnedSource(source,expected,'',{fetch:async()=>new Response(archive({extra}))}),e=>e.code===code);
});
test('public archive failure never falls back to API or a moving ref',async()=>{
 for(const status of [404,429,500]){let calls=0;await assert.rejects(capturePinnedSource(source,expected,'',{fetch:async()=>{calls++;return new Response('',{status});}}));assert.equal(calls,1);}
});


function acquire(body,options={}){return capturePinnedSource(source,expected,'',{fetch:async()=>new Response(body),...options});}
function changeRaw(change){return gzipSync(change(gunzipSync(archive())));}
test('archive framing, checksum, commit identity and compression are validated',async()=>{
 const cases=[
  [Buffer.from('not gzip'),'TEMPLATE_SOURCE_RESPONSE_INVALID'],
  [archive().subarray(0,-8),'TEMPLATE_SOURCE_RESPONSE_INVALID'],
  [changeRaw(raw=>{raw[0]^=1;return raw;}),'TEMPLATE_SOURCE_RESPONSE_INVALID'],
  [changeRaw(raw=>raw.subarray(0,-1024)),'TEMPLATE_SOURCE_RESPONSE_INVALID'],
  [changeRaw(raw=>Buffer.concat([raw,Buffer.from('hidden')])),'TEMPLATE_SOURCE_RESPONSE_INVALID'],
  [changeRaw(raw=>{raw[512+11]=98;return raw;}),'TEMPLATE_SOURCE_EXPECTATION_MISMATCH'],
  [changeRaw(raw=>{raw[512]=48;return raw;}),'TEMPLATE_SOURCE_RESPONSE_INVALID'],
 ];
 for(const [body,code] of cases)await assert.rejects(acquire(body),e=>e.code===code);
});
test('archive file limits, LFS, collisions and unsupported types fail closed',async()=>{
 for(const [path,data,type,code] of [
  ['large',Buffer.alloc(2*1024*1024+1),'0','TEMPLATE_SOURCE_LIMIT'],
  ['TEMPLATE.JSON',Buffer.from('{}'),'0','TEMPLATE_SOURCE_PATH_CONFLICT'],
  ['scripts',Buffer.from('file'),'0','TEMPLATE_SOURCE_PATH_CONFLICT'],
  ['lfs',Buffer.from('version https://git-lfs.github.com/spec/v1\n'),'0','TEMPLATE_SOURCE_LFS_UNSUPPORTED'],
  ['link',Buffer.alloc(0),'1','TEMPLATE_SOURCE_PATH_INVALID'],
  ['pipe',Buffer.alloc(0),'6','TEMPLATE_SOURCE_PATH_INVALID'],
 ])await assert.rejects(acquire(archive({extra:[entry(root+'/'+source.path+'/'+path,data,type)]})),e=>e.code===code);
 await assert.rejects(acquire(gzipSync(Buffer.alloc(128*1024*1024+1))),e=>e.code==='TEMPLATE_SOURCE_LIMIT');
 await assert.rejects(acquire(null,{fetch:async()=>new Response('',{headers:{'Content-Length':String(64*1024*1024+1)}})}),e=>e.code==='TEMPLATE_SOURCE_LIMIT');
});
test('cancellation never retries or yields a partial snapshot',async()=>{
 const controller=new AbortController();let requests=0;
 controller.abort(new Error('cancelled before request'));
 await assert.rejects(acquire(null,{signal:controller.signal,fetch:async()=>{requests++;return new Response(archive());}}),/cancelled before request/);assert.equal(requests,0);
 const downloading=new AbortController();
 await assert.rejects(acquire(null,{signal:downloading.signal,fetch:async()=>{requests++;return new Response(new ReadableStream({start(stream){stream.enqueue(archive());downloading.abort(new Error('cancelled during download'));stream.close();}}));}}),/cancelled during download/);assert.equal(requests,1);
});
test('temporary credentials stay on the API path without public fallback',async()=>{
 let calls=0;await assert.rejects(capturePinnedSource(source,expected,'temporary-token',{fetch:async(url,options)=>{calls++;assert.equal(new URL(url).hostname,'api.github.com');assert.equal(options.headers.Authorization,'Bearer temporary-token');assert.equal(options.redirect,'error');return new Response('{}',{status:401});}}),e=>e.code==='TEMPLATE_SOURCE_AUTH_REQUIRED');assert.equal(calls,1);
});
