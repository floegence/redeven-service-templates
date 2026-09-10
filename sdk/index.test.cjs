const {test}=require('node:test');
const assert=require('node:assert/strict');
const {createHash}=require('node:crypto');
const fs=require('node:fs');
const path=require('node:path');
const {sourceDigest,captureSource,discoverSources}=require('./index.cjs');

test('source digest is stable across languages and preserves executable modes',()=>{
 const fixture=JSON.parse(fs.readFileSync(path.join(__dirname,'../template/testdata/source-transfer.json'),'utf8'));
 assert.equal(sourceDigest(fixture.files),fixture.sha256);
 fixture.files[0].mode=fixture.files[0].mode==='100644'?'100755':'100644';
 assert.notEqual(sourceDigest(fixture.files),fixture.sha256);
});

test('capture uses a fixed commit, raw blobs and temporary API-only credentials',async()=>{
 const files=[{path:'redeven-service-template.json',mode:'100644',content:Buffer.from('{}').toString('base64')},{path:'start.sh',mode:'100755',content:Buffer.from('#!/bin/sh\n').toString('base64')}];
 const sha='a'.repeat(40);const blobs=new Map();
 const entries=files.map(f=>{const data=Buffer.from(f.content,'base64');const digest=createHash('sha1').update(`blob ${data.length}\0`).update(data).digest('hex');blobs.set(digest,data);return {path:f.path,mode:f.mode,type:'blob',sha:digest,size:data.length}});
 let calls=0;
 const fetch=async(url,options)=>{
  calls++;assert.equal(new URL(url).hostname,'api.github.com');assert.equal(options.headers.Authorization,'Bearer temporary-secret');assert.equal(options.redirect,'error');
  const endpoint=new URL(url).pathname;let result;
  if(endpoint==='/repos/example/templates') result={id:123,full_name:'example/templates',default_branch:'develop'};
  else if(endpoint==='/repos/example/templates/commits/develop')result={sha,commit:{tree:{sha}}};
  else if(endpoint==='/repos/example/templates/git/trees/'+sha)result={sha,tree:entries,truncated:false};
  else if(endpoint.startsWith('/repos/example/templates/git/blobs/')){const id=endpoint.split('/').at(-1);const data=blobs.get(id);assert.ok(data);result={sha:id,size:data.length,encoding:'base64',content:data.toString('base64')}}
  else assert.fail('Unplanned request: '+endpoint);
  return new Response(JSON.stringify(result),{status:200});
 };
 const result=await captureSource({repository:'https://github.com/example/templates'},'temporary-secret',{fetch});
 assert.equal(result.source.ref,'develop');assert.equal(result.source.commit_sha,sha);assert.deepEqual(result.files,files);assert.equal(result.sha256,sourceDigest(files));assert.equal(JSON.stringify(result).includes('temporary-secret'),false);assert.equal(calls,5);
 const catalog=await discoverSources({repository:'example/templates'},'temporary-secret',{fetch});assert.deepEqual(catalog.templates,[{path:'',entrypoint:'redeven-service-template.json'}]);
});

test('invalid source and unsafe file paths fail before network or persistence',async()=>{
 for(const repository of ['https://evil.invalid/a/b','https://token@github.com/a/b','https://github.com/a/b/releases/latest'])await assert.rejects(captureSource({repository},'',{fetch:()=>assert.fail('invalid source reached network')}));
 for(const file of [{path:'../escape',mode:'100644',content:''},{path:'link',mode:'120000',content:''},{path:'a',mode:'100644',content:Buffer.from('version https://git-lfs.github.com/spec/v1\n').toString('base64')}])assert.throws(()=>sourceDigest([file]));
});
