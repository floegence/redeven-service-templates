const {test}=require('node:test');const assert=require('node:assert/strict');const {createHash}=require('node:crypto');
const {sourceIdentity,capturePinnedSource,verifySourceExpectation,sourceDigest}=require('./index.cjs');
test('pinned acquisition never follows moving tracking ref and verifies transferred bytes',async()=>{
 const commit='a'.repeat(40),data=Buffer.from('{}'),blob=createHash('sha1').update('blob 2\0').update(data).digest('hex');
 const files=[{path:'redeven-service-template.json',mode:'100644',content:data.toString('base64')}];const expected={repository_id:123,commit_sha:commit,sha256:sourceDigest(files)};
 const fetch=async(url)=>{const endpoint=new URL(url).pathname;let result;
 if(endpoint==='/repos/example/templates')result={id:123,full_name:'example/templates',default_branch:'main'};
 else if(endpoint==='/repos/example/templates/commits/'+commit)result={sha:commit,commit:{tree:{sha:commit}}};
 else if(endpoint==='/repos/example/templates/git/trees/'+commit)result={sha:commit,tree:[{path:files[0].path,mode:'100644',type:'blob',sha:blob,size:2}]};
 else if(endpoint==='/repos/example/templates/git/blobs/'+blob)result={sha:blob,size:2,encoding:'base64',content:files[0].content};
 else assert.fail(endpoint);return new Response(JSON.stringify(result));};
 const s=await capturePinnedSource({repository:'example/templates',ref:'main'},expected,'',{fetch});assert.equal(s.source.ref,'main');verifySourceExpectation(s,expected);
 for(const changed of [{...expected,repository_id:124},{...expected,commit_sha:'b'.repeat(40)},{...expected,sha256:'c'.repeat(64)}])assert.throws(()=>verifySourceExpectation(s,changed));
 await assert.rejects(capturePinnedSource({repository:'example/templates',ref:'main'},{...expected,commit_sha:'main'},'',{fetch:()=>assert.fail('network')}));
});
test('source identity matches the established namespace',()=>{assert.equal(sourceIdentity(123,'web'),'git_'+createHash('sha256').update('123\0web\0').digest('hex').slice(0,48));assert.notEqual(sourceIdentity(123,'web'),sourceIdentity(124,'web'));});
