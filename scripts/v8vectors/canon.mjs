// Canonical JSON for JavaScript values, mirroring internal/v8/canon.go byte for byte.
//
// Numbers use ECMAScript Number::toString. Strings escape exactly as
// JSON.stringify (well-formed: lone surrogates become lowercase \udxxx).
// Values JSON cannot express use single-key tagged objects:
//   undefined -> {"$undefined":true}     array hole  -> {"$hole":true}
//   Date      -> {"$date":"<RFC3339Nano UTC>"}
//   BigInt    -> {"$bigint":"<dec>"}     non-finite and -0 -> {"$number":"NaN"|"Infinity"|"-Infinity"|"-0"}
//   bytes     -> {"$bytes":"<base64>"}   Map -> {"$map":[[k,v],...]}   Set -> {"$set":[...]}
//   RegExp    -> {"$regexp":[source,flags]}   Error -> {"$error":{"name":n,"message":m}}
//   wrapper   -> {"$wrapper":[kind,value]}
// A reference to an ancestor still being written (a cycle) is {"$cycle":<ancestor depth, root = 0>}.
// Shared, non-cyclic references are written out in full each time.

const str = (s) => JSON.stringify(s);

function dateString(d) {
  const ms = d.getTime();
  const year = d.getUTCFullYear();
  if (!Number.isFinite(ms) || year < 0 || year > 9999) {
    throw new RangeError('date outside RFC3339 range');
  }
  // toISOString always has three fractional digits; Go's RFC3339Nano trims trailing zeros.
  const iso = d.toISOString(); // YYYY-MM-DDTHH:MM:SS.mmmZ
  const base = iso.slice(0, 19);
  const frac = iso.slice(20, 23).replace(/0+$/, '');
  return frac === '' ? `${base}Z` : `${base}.${frac}Z`;
}

function number(n) {
  if (Number.isNaN(n)) return '{"$number":"NaN"}';
  if (n === Infinity) return '{"$number":"Infinity"}';
  if (n === -Infinity) return '{"$number":"-Infinity"}';
  if (Object.is(n, -0)) return '{"$number":"-0"}';
  return String(n);
}

function wrapperKind(v) {
  if (v instanceof Boolean) return 'Boolean';
  if (v instanceof Number) return 'Number';
  if (v instanceof String) return 'String';
  if (typeof v === 'object' && Object.prototype.toString.call(v) === '[object BigInt]') return 'BigInt';
  return null;
}

export function canonical(value) {
  const stack = [];
  const enc = (v) => {
    switch (typeof v) {
      case 'undefined': return '{"$undefined":true}';
      case 'boolean': return v ? 'true' : 'false';
      case 'number': return number(v);
      case 'bigint': return `{"$bigint":${str(v.toString())}}`;
      case 'string': return str(v);
      case 'object': break;
      default: throw new TypeError(`unsupported type ${typeof v}`);
    }
    if (v === null) return 'null';
    if (v instanceof Date) return `{"$date":${str(dateString(v))}}`;
    if (v instanceof RegExp) return `{"$regexp":[${str(v.source)},${str(v.flags)}]}`;
    if (v instanceof ArrayBuffer) return `{"$bytes":"${Buffer.from(v).toString('base64')}"}`;
    if (ArrayBuffer.isView(v)) {
      return `{"$bytes":"${Buffer.from(v.buffer, v.byteOffset, v.byteLength).toString('base64')}"}`;
    }
    if (v instanceof Error) return `{"$error":{"name":${str(v.name)},"message":${str(v.message)}}}`;
    const kind = wrapperKind(v);
    if (kind !== null) return `{"$wrapper":[${str(kind)},${enc(v.valueOf())}]}`;

    const depth = stack.indexOf(v);
    if (depth >= 0) return `{"$cycle":${depth}}`;
    stack.push(v);
    try {
      if (Array.isArray(v)) {
        const out = [];
        for (let i = 0; i < v.length; i++) out.push(i in v ? enc(v[i]) : '{"$hole":true}');
        return `[${out.join(',')}]`;
      }
      if (v instanceof Map) {
        return `{"$map":[${[...v].map(([k, x]) => `[${enc(k)},${enc(x)}]`).join(',')}]}`;
      }
      if (v instanceof Set) return `{"$set":[${[...v].map(enc).join(',')}]}`;
      return `{${Object.keys(v).map((k) => `${str(k)}:${enc(v[k])}`).join(',')}}`;
    } finally {
      stack.pop();
    }
  };
  return enc(value);
}
