// Only known locations and JSON types. Never enumerate arbitrary capability keys.
const record = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const own = (value, key) => record(value) && Object.hasOwn(value, key);
function kind(value, present) {
  if (!present) return 'missing';
  if (value === null) return 'null';
  if (Array.isArray(value)) return 'array';
  return ['string', 'number', 'boolean', 'object'].includes(typeof value) ? typeof value : 'other';
}

export function unobservedShape() {
  return { envelopeShape: 'not-observed', valueShape: 'not-observed', capabilitiesShape: 'not-observed',
    flatShape: 'not-observed', nestedContainerShape: 'not-observed', nestedShape: 'not-observed',
    candidateClass: 'not-observed', pathPredicate: 'not-observed' };
}

export function classifyCapabilityShape(envelope, normalize) {
  const valuePresent = own(envelope, 'value');
  const value = valuePresent ? envelope.value : undefined;
  const capsPresent = own(value, 'capabilities');
  const caps = capsPresent ? value.capabilities : undefined;
  const flatPresent = own(caps, 'msedge.userDataDir');
  const flat = flatPresent ? caps['msedge.userDataDir'] : undefined;
  const nestedContainerPresent = own(caps, 'msedge');
  const nestedContainer = nestedContainerPresent ? caps.msedge : undefined;
  const nestedPresent = own(nestedContainer, 'userDataDir');
  const nested = nestedPresent ? nestedContainer.userDataDir : undefined;
  let candidateClass;
  let pathPredicate = 'not-applicable';
  if (!record(envelope)) candidateClass = 'envelope-not-object';
  else if (!record(value)) candidateClass = 'value-not-object';
  else if (!record(caps)) candidateClass = 'capabilities-not-object';
  else if (flatPresent && nestedPresent) candidateClass = 'candidate-duplicated';
  else if (!flatPresent && !nestedPresent) candidateClass = 'candidate-missing';
  else {
    const candidate = flatPresent ? flat : nested;
    if (typeof candidate !== 'string') candidateClass = 'candidate-not-string';
    else {
      // Classification uses the same predicate evaluation as normalization.
      pathPredicate = 'accepted';
      const normalized = normalize(candidate, code => { pathPredicate = code; });
      candidateClass = normalized === null ? 'candidate-invalid-path' :
        flatPresent ? 'candidate-valid-flat' : 'candidate-valid-nested';
    }
  }
  return { envelopeShape: kind(envelope, true), valueShape: kind(value, valuePresent),
    capabilitiesShape: kind(caps, capsPresent), flatShape: kind(flat, flatPresent),
    nestedContainerShape: kind(nestedContainer, nestedContainerPresent), nestedShape: kind(nested, nestedPresent),
    candidateClass, pathPredicate };
}
