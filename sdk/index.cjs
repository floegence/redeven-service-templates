'use strict';

const {createHash}=require('node:crypto');
const posix=require('node:path').posix;
const FILENAME='redeven-service-template.json';
const MAX_FILE_BYTES=2*1024*1024;
const MAX_DIRECTORY_BYTES=16*1024*1024;
const MAX_FILES=1024;
const repoPattern=/^[a-zA-Z0-9_.-]+\/[a-zA-Z0-9_.-]+$/u;
const shaPattern=/^[a-f0-9]{40}$/u;

class TemplateSourceError extends Error {
 constructor(code,message){super(message);this.name='TemplateSourceError';this.code=code;}
}
const fail=(code,message)=>{throw new TemplateSourceError(code,message)};
function validPath(value){
 return typeof value==='string'&&value!==''&&Buffer.byteLength(value)<=1024&&!/[\p{Cc}\\:]/u.test(value)&&value.split('/').every(part=>part!==''&&part!=='.'&&part!=='..'&&part.toLowerCase()!=='.git'&&!/[. ]$/u.test(part));
}
function validRef(value){return typeof value==='string'&&value!==''&&Buffer.byteLength(value)<=256&&!/[\0\r\n\\?#]/u.test(value)&&!value.includes('..')}
function decodeContent(value){
 if(typeof value!=='string'||! /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/u.test(value.replace(/[\r\n]/gu,'')))fail('TEMPLATE_SOURCE_RESPONSE_INVALID','The template contains invalid file encoding.');
 return Buffer.from(value,'base64');
}
function sourceDigest(files){
 if(!Array.isArray(files)||files.length===0||files.length>MAX_FILES)fail('TEMPLATE_SOURCE_LIMIT','Template file count exceeds the supported limit.');
 const sorted=[...files].sort((a,b)=>Buffer.compare(Buffer.from(a.path),Buffer.from(b.path)));
 const seen=new Set();const hash=createHash('sha256');let size=0;
 for(const file of sorted){
  if(!validPath(file.path)||!['100644','100755'].includes(file.mode))fail('TEMPLATE_SOURCE_PATH_INVALID','Template sources must contain portable relative paths and regular files.');
  const key=file.path.toLowerCase();if(seen.has(key))fail('TEMPLATE_SOURCE_PATH_CONFLICT','Template source paths conflict.');seen.add(key);
  const data=decodeContent(file.content);size+=data.length;
  if(data.length>MAX_FILE_BYTES||size>MAX_DIRECTORY_BYTES)fail('TEMPLATE_SOURCE_LIMIT','Template source size exceeds the supported limit.');
  if(data.subarray(0,42).toString().startsWith('version https://git-lfs.github.com/spec/v1'))fail('TEMPLATE_SOURCE_LFS_UNSUPPORTED','Template sources must contain file bytes instead of Git LFS pointers.');
  hash.update(file.path+'\0'+file.mode+'\0'+createHash('sha256').update(data).digest('hex')+'\n');
 }
 for(const key of seen)for(let parent=posix.dirname(key);parent!=='.';parent=posix.dirname(parent))if(seen.has(parent))fail('TEMPLATE_SOURCE_PATH_CONFLICT','A template file is also used as a directory.');
 return hash.digest('hex');
}

function parseSource(input){
 let repository=String(input.repository??'').trim();let tail=[];
 if(repository.includes('://')){
  let url;try{url=new URL(repository)}catch{fail('TEMPLATE_SOURCE_INVALID','Use a GitHub HTTPS repository, directory, or template file link.')}
  if(url.protocol!=='https:'||url.host!=='github.com'||url.username||url.password||url.search||url.hash)fail('TEMPLATE_SOURCE_INVALID','Use a GitHub HTTPS repository, directory, or template file link.');
  let parts;try{parts=url.pathname.replace(/^\/+|\/+$/gu,'').split('/').map(decodeURIComponent)}catch{fail('TEMPLATE_SOURCE_INVALID','The GitHub link path is invalid.')}
  if(parts.length<2)fail('TEMPLATE_SOURCE_INVALID','A GitHub owner and repository are required.');repository=parts.slice(0,2).join('/');
  if(parts.length>2){if(parts.length<4||!['tree','blob'].includes(parts[2]))fail('TEMPLATE_SOURCE_INVALID','Use a repository path instead of a Release or archive link.');tail=parts.slice(3)}
 }
 repository=repository.replace(/\.git$/u,'');const ref=String(input.ref??'').trim();let path=String(input.path??'').trim().replace(/^\/+|\/+$/gu,'');path=directoryPath(path);
 if(!repoPattern.test(repository)||repository.includes('..')||repository.length>240||(ref&&!validRef(ref))||(path&&!validPath(path))||tail.length>32||tail.some(part=>!validPath(part)))fail('TEMPLATE_SOURCE_INVALID','The GitHub repository, ref, or template path is invalid.');
 return {repository,ref,path,tail};
}
function directoryPath(path){if([FILENAME,'template.json'].includes(path))return '';if(path.endsWith('/'+FILENAME)||path.endsWith('/template.json'))return posix.dirname(path);return path}

async function request(repository,endpoint,token,options){
 const signal=options.signal?AbortSignal.any([options.signal,AbortSignal.timeout(45000)]):AbortSignal.timeout(45000);
 const headers={Accept:'application/vnd.github+json','X-GitHub-Api-Version':'2022-11-28','User-Agent':'Redeven-Service-Templates'};
 if(token)headers.Authorization='Bearer '+token;
 let response;
 try{response=await (options.fetch??globalThis.fetch)('https://api.github.com/repos/'+repository+endpoint,{headers,signal,redirect:'error'})}catch{if(options.signal?.aborted)throw options.signal.reason;fail('TEMPLATE_SOURCE_UNREACHABLE','GitHub could not be reached from the selected download location.')}
 if(response.status!==200){
  if(response.status===401)fail('TEMPLATE_SOURCE_AUTH_REQUIRED','GitHub rejected the temporary repository credential.');
  if([403,429].includes(response.status)){
   if(response.status===429||response.headers.get('X-RateLimit-Remaining')==='0'||response.headers.has('Retry-After'))fail('TEMPLATE_SOURCE_RATE_LIMITED','GitHub has temporarily limited repository requests. Try again later.');
   fail('TEMPLATE_SOURCE_ACCESS_DENIED',"GitHub denied access. Verify the token's repository read permission.");
  }
  if(response.status===404)fail('TEMPLATE_SOURCE_NOT_FOUND','The repository, ref, or template path was not found, or the credential cannot access it.');
  fail('TEMPLATE_SOURCE_UNAVAILABLE','GitHub could not return the template source.');
 }
 const chunks=[];let size=0;
 try{for await(const chunk of response.body){size+=chunk.length;if(size>6*1024*1024)fail('TEMPLATE_SOURCE_LIMIT','The GitHub response exceeds the template source limit.');chunks.push(chunk)}}catch(error){if(error instanceof TemplateSourceError)throw error;if(options.signal?.aborted)throw options.signal.reason;fail('TEMPLATE_SOURCE_UNREACHABLE','The GitHub response was interrupted.')}
 try{return JSON.parse(Buffer.concat(chunks).toString('utf8'))}catch{fail('TEMPLATE_SOURCE_RESPONSE_INVALID','GitHub returned an invalid source response.')}
}

async function resolveSource(input,token='',options={}){
 const parsed=parseSource(input);let {repository,ref,path,tail}=parsed;
 const repo=await request(repository,'',token,options);
 if(!Number.isSafeInteger(repo.id)||repo.id<1||!repoPattern.test(repo.full_name)||repo.full_name.toLowerCase()!==repository.toLowerCase()||!validRef(repo.default_branch))fail('TEMPLATE_SOURCE_RESPONSE_INVALID','GitHub returned an invalid repository identity.');
 let commit;
 if(tail.length){
  if(ref||path)fail('TEMPLATE_SOURCE_INVALID','Use the GitHub path link or separate ref and path fields, not both.');
  for(let cut=tail.length;cut>0;cut--){const candidate=tail.slice(0,cut).join('/');if(!validRef(candidate))continue;try{commit=await request(repository,'/commits/'+encodeURIComponent(candidate),token,options)}catch(error){if(error.code==='TEMPLATE_SOURCE_NOT_FOUND')continue;throw error}ref=candidate;path=directoryPath(tail.slice(cut).join('/'));break}
  if(!commit)fail('TEMPLATE_SOURCE_NOT_FOUND','The GitHub link does not resolve to an accessible ref.');
 }else{ref=ref||repo.default_branch;commit=await request(repository,'/commits/'+encodeURIComponent(ref),token,options)}
 if(!shaPattern.test(commit.sha)||!shaPattern.test(commit.commit?.tree?.sha))fail('TEMPLATE_SOURCE_RESPONSE_INVALID','GitHub returned an invalid commit identity.');
 return {repository:repo.full_name,repository_id:repo.id,ref,path,commit_sha:commit.sha,tree_sha:commit.commit.tree.sha};
}

async function tree(source,sha,token,options){
 if(!shaPattern.test(sha))fail('TEMPLATE_SOURCE_RESPONSE_INVALID','GitHub returned an invalid directory identity.');
 const result=await request(source.repository,'/git/trees/'+sha,token,options);
 if(result.truncated||!Array.isArray(result.tree)||result.tree.length>MAX_FILES)fail('TEMPLATE_SOURCE_LIMIT','The repository directory listing is incomplete or exceeds the template limit.');
 if(result.sha!==sha)fail('TEMPLATE_SOURCE_RESPONSE_INVALID','GitHub returned a different directory identity.');return result.tree;
}
async function directory(source,token,options){
 let entries=await tree(source,source.tree_sha,token,options);
 if(source.path)for(const part of source.path.split('/')){const child=entries.find(entry=>entry.path===part&&entry.type==='tree');if(!child)fail('TEMPLATE_SOURCE_NOT_FOUND','The selected template directory does not exist in this commit.');entries=await tree(source,child.sha,token,options)}
 return entries;
}

async function discoverSources(input,token='',options={}){
 const source=await resolveSource(input,token,options);const entries=await directory(source,token,options);const result={source,templates:[]};
 const entrypoint=items=>[FILENAME,'template.json'].find(name=>items.some(item=>item.path===name&&item.type==='blob'));
 const name=entrypoint(entries);if(name){result.templates.push({path:source.path,entrypoint:name});return result}
 const root=entries.find(entry=>entry.path==='templates'&&entry.type==='tree');if(!root)return result;
 const children=await tree(source,root.sha,token,options);if(children.length>100)fail('TEMPLATE_SOURCE_LIMIT','Choose an explicit template path in repositories with more than 100 template directories.');
 for(const child of children){if(child.type!=='tree'||!validPath(child.path))continue;const items=await tree(source,child.sha,token,options);const name=entrypoint(items);if(name)result.templates.push({path:posix.join(source.path,'templates',child.path),entrypoint:name})}
 return result;
}

async function captureSource(input,token='',options={}){
 const source=await resolveSource(input,token,options);const files=[];let total=0;
 const walk=async(entries,prefix,depth)=>{
  if(depth>32)fail('TEMPLATE_SOURCE_LIMIT','The template directory nesting exceeds its limit.');
  for(const entry of entries){
   const name=posix.join(prefix,entry.path);if(!validPath(entry.path)||!validPath(name)||!shaPattern.test(entry.sha))fail('TEMPLATE_SOURCE_PATH_INVALID','GitHub returned an invalid source path or object identity.');
   if(entry.type==='tree'&&entry.mode==='040000'){await walk(await tree(source,entry.sha,token,options),name,depth+1);continue}
   if(entry.type!=='blob'||!['100644','100755'].includes(entry.mode))fail('TEMPLATE_SOURCE_PATH_INVALID','Template sources cannot contain symlinks or submodules.');
   total+=entry.size;if(!Number.isSafeInteger(entry.size)||entry.size<0||entry.size>MAX_FILE_BYTES||total>MAX_DIRECTORY_BYTES||files.length>=MAX_FILES)fail('TEMPLATE_SOURCE_LIMIT','The template directory exceeds its file or byte limit.');
   const blob=await request(source.repository,'/git/blobs/'+entry.sha,token,options);const data=decodeContent(blob.content);
   if(blob.encoding!=='base64'||blob.sha!==entry.sha||blob.size!==entry.size||data.length!==entry.size)fail('TEMPLATE_SOURCE_RESPONSE_INVALID','GitHub returned inconsistent file bytes.');
   if(createHash('sha1').update(`blob ${data.length}\0`).update(data).digest('hex')!==entry.sha)fail('TEMPLATE_SOURCE_DIGEST_MISMATCH','A GitHub blob did not match its declared identity.');
   files.push({path:name,mode:entry.mode,content:data.toString('base64')});options.onProgress?.({phase:'downloading',files:files.length,bytes:total});
  }
 };
 await walk(await directory(source,token,options),'',0);files.sort((a,b)=>Buffer.compare(Buffer.from(a.path),Buffer.from(b.path)));
 return {source,files,sha256:sourceDigest(files)};
}

module.exports={FILENAME,MAX_FILE_BYTES,MAX_DIRECTORY_BYTES,MAX_FILES,TemplateSourceError,sourceDigest,resolveSource,discoverSources,captureSource};
