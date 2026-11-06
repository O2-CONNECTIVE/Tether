// Synthetic values only; no retained candidate is reconstructed or printed.
import test from 'node:test';
import assert from 'node:assert/strict';
import { normalizeProfilePath } from './path-validation.mjs';
import { classifyCapabilityShape } from './capability-shape.mjs';
const classify = capabilities => classifyCapabilityShape({value:{capabilities}},normalizeProfilePath);
test('absolute root and EBWebView child pass prefix but remain different identities',()=>{
  const root=normalizeProfilePath('C:/synthetic/profile');
  const child=normalizeProfilePath('C:/synthetic/profile/EBWebView');
  assert.ok(root);assert.ok(child);assert.notEqual(root,child);
  for(const candidate of ['C:/synthetic/profile','C:/synthetic/profile/EBWebView']) {
    const shape=classify({msedge:{userDataDir:candidate}});
    assert.equal(shape.pathPredicate,'accepted');assert.equal(shape.candidateClass,'candidate-valid-nested');
  }
});
test('empty relative and UNC synthetic values share a rejection without establishing a real value',()=>{
  for(const candidate of ['', 'relative', '\\\\synthetic\\share', 'C:relative']) {
    const shape=classify({msedge:{userDataDir:candidate}});
    assert.equal(shape.nestedShape,'string');assert.equal(shape.pathPredicate,'drive-prefix');
    assert.equal(shape.candidateClass,'candidate-invalid-path');assert.equal(normalizeProfilePath(candidate),null);
  }
});
test('flat nested duplicate missing and non-string retain exact ambiguity handling',()=>{
  const path='C:/synthetic/profile';
  assert.equal(classify({'msedge.userDataDir':path}).candidateClass,'candidate-valid-flat');
  assert.equal(classify({msedge:{userDataDir:path}}).candidateClass,'candidate-valid-nested');
  for(const nested of [path,'',null]) {
    const shape=classify({'msedge.userDataDir':path,msedge:{userDataDir:nested}});
    assert.equal(shape.candidateClass,'candidate-duplicated');assert.equal(shape.pathPredicate,'not-applicable');
  }
  assert.equal(classify({}).candidateClass,'candidate-missing');
  assert.equal(classify({msedge:{userDataDir:null}}).candidateClass,'candidate-not-string');
});
