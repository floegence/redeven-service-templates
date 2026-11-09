'use strict';
const {gunzipSync}=require('node:zlib');
// Parse only bounded GitHub ustar/PAX bytes in memory. Never extract paths.
module.exports=async function publicArchive(source,expected,options,contract){
 const {fail,validPath,sourceDigest}=contract;
 const invalid=()=>fail('TEMPLATE_SOURCE_RESPONSE_INVALID','GitHub returned an invalid source archive.');
 const limit=()=>fail('TEMPLATE_SOURCE_LIMIT','The source archive exceeds its bounded download limit.');
 const signal=options.signal?AbortSignal.any([options.signal,AbortSignal.timeout(45000)]):AbortSignal.timeout(45000);
 signal.throwIfAborted();
 let response;
 try{response=await (options.fetch??globalThis.fetch)(`https://codeload.github.com/${source.repository}/tar.gz/${expected.commit_sha}`,{headers:{'User-Agent':'Redeven-Service-Templates'},signal,redirect:'error'});}catch{if(options.signal?.aborted)throw options.signal.reason;fail('TEMPLATE_SOURCE_UNREACHABLE','GitHub could not be reached from the selected download location.');}
 if(response.status!==200){
  await response.body?.cancel();
  if(response.status===429)fail('TEMPLATE_SOURCE_RATE_LIMITED','GitHub has temporarily limited repository requests. Try again later.');
  if(response.status===404)fail('TEMPLATE_SOURCE_NOT_FOUND','The public commit archive was not found. Private repositories require a temporary token.');
  fail('TEMPLATE_SOURCE_UNAVAILABLE','GitHub could not return the public source archive.');
 }
 if(Number(response.headers.get('Content-Length'))>64*1024*1024){await response.body?.cancel();limit();}
 const chunks=[];let size=0;
 try{for await(const chunk of response.body){signal.throwIfAborted();size+=chunk.length;if(size>64*1024*1024)limit();chunks.push(chunk);}}catch(error){if(error.code?.startsWith('TEMPLATE_'))throw error;if(options.signal?.aborted)throw options.signal.reason;fail('TEMPLATE_SOURCE_UNREACHABLE','The source archive download was interrupted.');}
 let raw;try{raw=gunzipSync(Buffer.concat(chunks),{maxOutputLength:128*1024*1024});}catch(error){if(error.code==='ERR_BUFFER_TOO_LARGE')limit();invalid();}
 signal.throwIfAborted();
 const decoder=new TextDecoder('utf-8',{fatal:true});
 const text=bytes=>{try{return decoder.decode(bytes);}catch{invalid();}};
 const field=bytes=>text(bytes.subarray(0,bytes.indexOf(0)<0?bytes.length:bytes.indexOf(0)));
 const number=bytes=>{const s=field(bytes).trim();if(!/^[0-7]+$/.test(s))invalid();const n=parseInt(s,8);if(!Number.isSafeInteger(n))invalid();return n;};
 const pax=data=>{const result={};let at=0;while(at<data.length){const space=data.indexOf(32,at);if(space<0||space-at>10)invalid();const value=text(data.subarray(at,space));if(!/^[1-9][0-9]*$/.test(value))invalid();const end=at+Number(value);if(end>data.length||end<=space+2||data[end-1]!==10)invalid();const record=text(data.subarray(space+1,end-1)),eq=record.indexOf('=');if(eq<1)invalid();const key=record.slice(0,eq);if(Object.hasOwn(result,key)||!['comment','path','mtime','atime','ctime'].includes(key))invalid();result[key]=record.slice(eq+1);at=end;}return result;};
 const root=source.repository.split('/')[1]+'-'+expected.commit_sha;
 const prefix=root+'/'+(source.path?source.path+'/':'');
 const files=[];const seen=new Set();let total=0,offset=0,count=0,global={},local={},ended=false;
 while(offset+512<=raw.length){
  const header=raw.subarray(offset,offset+512);offset+=512;
  if(header.every(byte=>byte===0)){if(raw.length-offset<512||raw.subarray(offset).some(byte=>byte!==0))invalid();ended=true;break;}
  if(++count>100000)limit();
  const checksum=number(header.subarray(148,156));let sum=0;for(let i=0;i<512;i++)sum+=(i>=148&&i<156)?32:header[i];if(sum!==checksum||field(header.subarray(257,263))!=='ustar')invalid();
  const length=number(header.subarray(124,136)),mode=number(header.subarray(100,108)),type=String.fromCharCode(header[156]||48);
  if(offset+Math.ceil(length/512)*512>raw.length)invalid();
  const data=raw.subarray(offset,offset+length);offset+=Math.ceil(length/512)*512;
  if(type==='g'||type==='x'){if(length>65536)limit();const values=pax(data);if(type==='g'){if(count!==1||Object.keys(global).length||values.path)invalid();global=values;}else{if(Object.keys(local).length||values.comment)invalid();local=values;}continue;}
  const headPrefix=field(header.subarray(345,500));const name=local.path??(headPrefix?headPrefix+'/':'')+field(header.subarray(0,100));local={};
  if(global.comment!==expected.commit_sha)fail('TEMPLATE_SOURCE_EXPECTATION_MISMATCH','The archive commit differs from the selected identity.');
  const clean=name.endsWith('/')?name.slice(0,-1):name;
  if(!validPath(clean)||!(clean===root||clean.startsWith(root+'/')))fail('TEMPLATE_SOURCE_PATH_INVALID','The source archive contains an invalid path.');
  if(clean===root||!name.startsWith(prefix))continue;
  const relative=clean.slice(prefix.length);
  if(!relative)continue;
  if(type==='5')continue;
  if(type!=='0'||!validPath(relative)||relative.split('/').length>33)fail('TEMPLATE_SOURCE_PATH_INVALID','Template sources must contain portable relative paths and regular files.');
  if(seen.has(relative.toLowerCase()))fail('TEMPLATE_SOURCE_PATH_CONFLICT','Template source paths conflict.');seen.add(relative.toLowerCase());
  total+=length;if(length>2*1024*1024||total>16*1024*1024||files.length>=1024)limit();
  files.push({path:relative,mode:mode&0o111?'100755':'100644',content:data.toString('base64')});options.onProgress?.({phase:'downloading',files:files.length,bytes:total});
 }
 if(!ended||Object.keys(local).length)invalid();
 files.sort((a,b)=>Buffer.compare(Buffer.from(a.path),Buffer.from(b.path)));
 return {source:{repository:source.repository,repository_id:expected.repository_id,ref:source.ref,path:source.path,commit_sha:expected.commit_sha},files,sha256:sourceDigest(files)};
};
