// fullstack templates: renders the pages of a view.Engine template pack in
// the browser. The pack holds html/template's trees after escaping, so
// every action already ends in the escaper of its context; this file
// implements text/template's evaluation and html/template's escapers the
// way Go does. Whatever it cannot do the same way throws Unsupported, and
// the page is fetched from the server instead.

export class Unsupported extends Error {}

const NOVALUE = Symbol("no value");
const FAILSAFE = "ZgotmplZ";

// ---------------------------------------------------------------- values

class Typed { // html/template's typed strings: HTML, URL, JS…
  constructor(t, s) { this.t = t; this.s = s; }
}
class GoMap {
  constructor(m) { this.m = m; }
  keys() { return Object.keys(this.m).sort(cmpBytes); }
  get(k) { return Object.prototype.hasOwnProperty.call(this.m, k) ? this.m[k] : NOVALUE; }
  get size() { return Object.keys(this.m).length; }
}
class GoStruct {
  constructor(f, str, json) { this.f = f; this.str = str; this.json = json; }
}
class Stringer { // a non-struct value with a String method
  constructor(str, v) { this.str = str; this.v = v; }
}
class GoFloat { // a whole float64 from 1e6 up (prints as 1e+06)
  constructor(f) { this.f = f; }
}
class BigInt64 { // an integer beyond JS numbers, kept as its digits
  constructor(s) { this.s = s; }
}
class Opaque {
  constructor(kind) { this.kind = kind; }
}

const typedKeys = { $h: "h", $ha: "ha", $j: "j", $js: "js", $u: "u", $c: "c", $ss: "ss" };

export function decode(v) {
  if (v === null || typeof v !== "object") return v;
  if (Array.isArray(v)) return v.map(decode);
  if ("$m" in v) {
    const m = {};
    for (const k of Object.keys(v.$m)) m[k] = decode(v.$m[k]);
    return new GoMap(m);
  }
  if ("$s" in v) {
    const f = {};
    for (const k of Object.keys(v)) if (k[0] !== "$") f[k] = decode(v[k]);
    return new GoStruct(f, "$p" in v ? v.$p : undefined, "$J" in v ? v.$J : undefined);
  }
  if ("$x" in v) return new Opaque(v.$x);
  if ("$f" in v) return new GoFloat(v.$f);
  if ("$i" in v) return new BigInt64(v.$i);
  if ("$p" in v) return new Stringer(v.$p, decode(v.$v));
  for (const k in typedKeys) if (k in v) return new Typed(typedKeys[k], v[k]);
  throw new Unsupported("unknown value");
}

function cmpBytes(a, b) { // Go sorts strings by bytes; for BMP text this is code unit order
  return a < b ? -1 : a > b ? 1 : 0;
}

const utf8 = new TextEncoder();

function truth(v) {
  if (v === null || v === undefined || v === NOVALUE) return false;
  switch (typeof v) {
    case "boolean": return v;
    case "number": return v !== 0;
    case "string": return v.length > 0;
  }
  if (Array.isArray(v)) return v.length > 0;
  if (v instanceof GoMap) return v.size > 0;
  if (v instanceof Typed) return v.s.length > 0;
  if (v instanceof Stringer) return truth(v.v);
  if (v instanceof GoFloat) return v.f !== 0;
  if (v instanceof BigInt64) return v.s !== "0";
  if (v instanceof GoStruct) return true;
  throw new Unsupported("truth of " + (v.kind || typeof v));
}

// ---------------------------------------------------------------- fmt

// goNumber prints a JSON number the way fmt prints the Go value: whole
// numbers are ints (whole floats from 1e6 up come as GoFloat), others are
// floats in %g's shortest form.
function goNumber(x) {
  return Number.isInteger(x) && !Object.is(x, -0) ? String(x) : goFloat(x);
}

function goFloat(x) {
  if (Number.isNaN(x)) return "NaN";
  if (x === Infinity) return "+Inf";
  if (x === -Infinity) return "-Inf";
  if (Object.is(x, -0)) return "-0";
  const [mant, e] = x.toExponential().split("e");
  const exp = Number(e);
  if (exp < -4 || exp >= 6) {
    return mant + "e" + (exp < 0 ? "-" : "+") + String(Math.abs(exp)).padStart(2, "0");
  }
  return String(x);
}

// format is fmt's %v.
function format(v, top = true) {
  if (v === null || v === undefined || v === NOVALUE) return "<nil>";
  switch (typeof v) {
    case "boolean": return v ? "true" : "false";
    case "number": return goNumber(v);
    case "string": return v;
  }
  if (v instanceof Typed) return v.s;
  if (v instanceof GoFloat) return goFloat(v.f);
  if (v instanceof BigInt64) return v.s;
  if (v instanceof Stringer) return v.str;
  if (Array.isArray(v)) return "[" + v.map((x) => format(x, false)).join(" ") + "]";
  if (v instanceof GoMap) return "map[" + v.keys().map((k) => k + ":" + format(v.m[k], false)).join(" ") + "]";
  if (v instanceof GoStruct && v.str !== undefined) return v.str;
  throw new Unsupported("printing a " + (v.constructor && v.constructor.name));
}

function isStringKind(v) {
  return typeof v === "string" || v instanceof Typed;
}

function sprint(args) {
  let out = "";
  args.forEach((a, i) => {
    if (i > 0 && !isStringKind(a) && !isStringKind(args[i - 1])) out += " ";
    out += format(a);
  });
  return out;
}

function sprintln(args) {
  return args.map((a) => format(a)).join(" ") + "\n";
}

function quote(s) {
  if (/[\u0000-\u001f\u007f]/.test(s)) throw new Unsupported("%q of control characters");
  return '"' + s.replace(/\\/g, "\\\\").replace(/"/g, '\\"') + '"';
}

function sprintf(fmtStr, args) {
  let out = "", ai = 0;
  for (let i = 0; i < fmtStr.length; i++) {
    const c = fmtStr[i];
    if (c !== "%") { out += c; continue; }
    const m = /^([-+ 0#]*)(\d*)(?:\.(\d*))?([a-zA-Z%])/.exec(fmtStr.slice(i + 1));
    if (!m) throw new Unsupported("printf format");
    i += m[0].length;
    const [, flags, width, prec, verb] = m;
    if (verb === "%") { out += "%"; continue; }
    if (ai >= args.length) throw new Unsupported("printf: missing argument");
    let a = args[ai++];
    if (a instanceof Stringer && verb !== "v" && verb !== "s") a = a.v;
    if (a instanceof BigInt64) {
      if (verb !== "v" && verb !== "d") throw new Unsupported("printf of a 64-bit integer");
      out += a.s; continue;
    }
    if (a instanceof GoFloat && verb !== "v") a = a.f;
    let s;
    switch (verb) {
      case "v": s = format(a); break;
      case "s":
        if (!isStringKind(a) && !(a instanceof Stringer)) throw new Unsupported("%s of a non-string");
        s = format(a); break;
      case "q": if (!isStringKind(a)) throw new Unsupported("%q"); s = quote(format(a)); break;
      case "t": if (typeof a !== "boolean") throw new Unsupported("%t"); s = String(a); break;
      case "d":
        if (typeof a !== "number" || !Number.isInteger(a)) throw new Unsupported("%d of a non-integer");
        s = String(Math.abs(a)); if (a < 0) s = "-" + s; else if (flags.includes("+")) s = "+" + s; break;
      case "x": case "X":
        if (typeof a === "number" && Number.isInteger(a)) s = (a < 0 ? "-" : "") + Math.abs(a).toString(16);
        else if (typeof a === "string") s = [...utf8.encode(a)].map((b) => b.toString(16).padStart(2, "0")).join("");
        else throw new Unsupported("%x");
        if (verb === "X") s = s.toUpperCase();
        break;
      case "f": case "F":
        if (typeof a !== "number") throw new Unsupported("%f");
        s = a.toFixed(prec === undefined ? 6 : Number(prec || 0));
        if (flags.includes("+") && a >= 0) s = "+" + s; break;
      case "c": if (typeof a !== "number") throw new Unsupported("%c"); s = String.fromCodePoint(a); break;
      default: throw new Unsupported("printf %" + verb);
    }
    if (width) {
      const w = Number(width);
      if (s.length < w) {
        if (flags.includes("-")) s = s.padEnd(w);
        else if (flags.includes("0") && /[dfFxX]/.test(verb)) s = (s[0] === "-" ? "-" + s.slice(1).padStart(w - 1, "0") : s.padStart(w, "0"));
        else s = s.padStart(w);
      }
    }
    out += s;
  }
  if (ai < args.length) throw new Unsupported("printf: extra arguments");
  return out;
}

// ---------------------------------------------------------------- escapers

function stringify(args) {
  if (args.length === 1) {
    const a = args[0];
    if (typeof a === "string") return [a, ""];
    if (a instanceof Typed) return [a.s, a.t];
  }
  const rest = args.filter((a) => a !== null && a !== undefined && a !== NOVALUE);
  return [sprint(rest), ""];
}

const htmlTable = { 0: "�", 34: "&#34;", 38: "&amp;", 39: "&#39;", 43: "&#43;", 60: "&lt;", 62: "&gt;" };
const htmlNormTable = { 0: "�", 34: "&#34;", 39: "&#39;", 43: "&#43;", 60: "&lt;", 62: "&gt;" };
const htmlNospaceTable = {
  0: "&#xfffd;", 9: "&#9;", 10: "&#10;", 11: "&#11;", 12: "&#12;", 13: "&#13;", 32: "&#32;", 34: "&#34;",
  38: "&amp;", 39: "&#39;", 43: "&#43;", 60: "&lt;", 61: "&#61;", 62: "&gt;", 96: "&#96;",
};

function htmlReplacer(s, table, badRunes) {
  let out = "";
  for (const ch of s) {
    const cp = ch.codePointAt(0);
    if (cp in table) out += table[cp];
    else if (!badRunes && (cp >= 0xfdd0 && cp <= 0xfdef || cp >= 0xfff0 && cp <= 0xffff)) out += "&#x" + cp.toString(16) + ";";
    else out += ch;
  }
  return out;
}

const low = {
  0: "\\u0000", 1: "\\u0001", 2: "\\u0002", 3: "\\u0003", 4: "\\u0004", 5: "\\u0005", 6: "\\u0006", 7: "\\u0007",
  8: "\\u0008", 9: "\\t", 10: "\\n", 11: "\\u000b", 12: "\\f", 13: "\\r", 14: "\\u000e", 15: "\\u000f",
  16: "\\u0010", 17: "\\u0011", 18: "\\u0012", 19: "\\u0013", 20: "\\u0014", 21: "\\u0015", 22: "\\u0016",
  23: "\\u0017", 24: "\\u0018", 25: "\\u0019", 26: "\\u001a", 27: "\\u001b", 28: "\\u001c", 29: "\\u001d",
  30: "\\u001e", 31: "\\u001f",
};
const jsStrTable = {
  34: "\\u0022", 96: "\\u0060", 38: "\\u0026", 39: "\\u0027", 43: "\\u002b", 47: "\\/", 60: "\\u003c", 62: "\\u003e", 92: "\\\\",
};
const jsBqTable = { ...jsStrTable, 36: "\\u0024", 123: "\\u007b", 125: "\\u007d" };
const jsStrNormTable = { 34: "\\u0022", 38: "\\u0026", 39: "\\u0027", 96: "\\u0060", 43: "\\u002b", 47: "\\/", 60: "\\u003c", 62: "\\u003e" };
const jsRegexpTable = {
  34: "\\u0022", 36: "\\$", 38: "\\u0026", 39: "\\u0027", 40: "\\(", 41: "\\)", 42: "\\*", 43: "\\u002b", 45: "\\-",
  46: "\\.", 47: "\\/", 60: "\\u003c", 62: "\\u003e", 63: "\\?", 91: "\\[", 92: "\\\\", 93: "\\]", 94: "\\^",
  123: "\\{", 124: "\\|", 125: "\\}",
};

function replace(s, table) {
  let out = "";
  for (const ch of s) {
    const cp = ch.codePointAt(0);
    if (cp in low) out += low[cp];
    else if (cp in table) out += table[cp];
    else if (cp === 0x2028) out += "\\u2028";
    else if (cp === 0x2029) out += "\\u2029";
    else out += ch;
  }
  return out;
}

function isJSIdentPart(c) {
  return c === "$" || c === "_" || c >= "0" && c <= "9" || c >= "a" && c <= "z" || c >= "A" && c <= "Z";
}

function jsonString(s) {
  let out = '"';
  for (const ch of s) {
    const cp = ch.codePointAt(0);
    switch (ch) {
      case '"': out += '\\"'; continue;
      case "\\": out += "\\\\"; continue;
      case "\n": out += "\\n"; continue;
      case "\r": out += "\\r"; continue;
      case "\t": out += "\\t"; continue;
      case "\b": out += "\\b"; continue;
      case "\f": out += "\\f"; continue;
      case "<": out += "\\u003c"; continue;
      case ">": out += "\\u003e"; continue;
      case "&": out += "\\u0026"; continue;
    }
    if (cp < 0x20 || cp === 0x2028 || cp === 0x2029) out += "\\u" + cp.toString(16).padStart(4, "0");
    else out += ch;
  }
  return out + '"';
}

function jsonNumber(x) {
  if (!Number.isFinite(x)) throw new Unsupported("json of " + x);
  const abs = Math.abs(x);
  if (abs !== 0 && (abs < 1e-6 || abs >= 1e21)) {
    let s = x.toExponential();
    const [m, e] = s.split("e");
    const n = Number(e);
    return m + "e" + (n < 0 ? "-" : "+") + String(Math.abs(n)).padStart(2, "0");
  }
  return String(x);
}

// goJSON is encoding/json's Marshal of a decoded value.
export function goJSON(v) {
  if (v === null || v === undefined || v === NOVALUE) return "null";
  switch (typeof v) {
    case "boolean": return v ? "true" : "false";
    case "number": return jsonNumber(v);
    case "string": return jsonString(v);
  }
  if (v instanceof Typed) return jsonString(v.s);
  if (v instanceof Stringer) return goJSON(v.v);
  if (v instanceof GoFloat) return jsonNumber(v.f);
  if (v instanceof BigInt64) return v.s;
  if (Array.isArray(v)) return "[" + v.map(goJSON).join(",") + "]";
  if (v instanceof GoMap) return "{" + v.keys().map((k) => jsonString(k) + ":" + goJSON(v.m[k])).join(",") + "}";
  if (v instanceof GoStruct && v.json !== undefined) return v.json;
  throw new Unsupported("JSON of a struct that encoding/json cannot marshal");
}

function jsValEscaper(args) {
  let a;
  if (args.length === 1) {
    a = args[0];
    if (a instanceof Typed && a.t === "j") return a.s;
    if (a instanceof Typed && a.t === "js") return '"' + a.s + '"';
    if (a instanceof Stringer) a = a.str;
  } else {
    a = sprint(args);
  }
  const b = goJSON(a);
  return isJSIdentPart(b[0]) || isJSIdentPart(b[b.length - 1]) ? " " + b + " " : b;
}

function isSafeURL(s) {
  const i = s.indexOf(":");
  if (i >= 0 && !s.slice(0, i).includes("/")) {
    const p = s.slice(0, i).toLowerCase();
    if (p !== "http" && p !== "https" && p !== "mailto") return false;
  }
  return true;
}

function isHexByte(c) {
  return c >= 48 && c <= 57 || c >= 97 && c <= 102 || c >= 65 && c <= 70;
}

// processURL is processURLOnto: bytes outside the allowed set become %xx.
function processURL(s, norm) {
  const b = utf8.encode(s);
  let out = "", changed = false;
  for (let i = 0; i < b.length; i++) {
    const c = b[i], ch = String.fromCharCode(c);
    if ("!#$&*+,/:;=?@[]".includes(ch) && c < 128) {
      if (norm) { out += ch; continue; }
    } else if ("-._~".includes(ch) && c < 128) {
      out += ch; continue;
    } else if (c === 37) {
      if (norm && i + 2 < b.length && isHexByte(b[i + 1]) && isHexByte(b[i + 2])) { out += ch; continue; }
    } else if (c >= 97 && c <= 122 || c >= 65 && c <= 90 || c >= 48 && c <= 57) {
      out += ch; continue;
    }
    out += "%" + c.toString(16).padStart(2, "0");
    changed = true;
  }
  return changed ? out : s;
}

function urlProcessor(norm, args) {
  const [s, t] = stringify(args);
  return processURL(s, norm || t === "u");
}

function isHTMLSpace(c) {
  return c === 9 || c === 10 || c === 12 || c === 13 || c === 32;
}

function srcsetEscaper(args) {
  const [s, t] = stringify(args);
  if (t === "ss") return s;
  if (t === "u") return processURL(s, true).replace(/,/g, "%2c");
  if (/[^\x00-\x7f]/.test(s)) throw new Unsupported("srcset with non-ASCII text");
  const parts = s.split(",");
  return parts.map((part) => {
    let start = 0;
    while (start < part.length && isHTMLSpace(part.charCodeAt(start))) start++;
    let end = part.length;
    for (let i = start; i < part.length; i++) if (isHTMLSpace(part.charCodeAt(i))) { end = i; break; }
    const url = part.slice(start, end);
    if (isSafeURL(url) && /^[\t\n\f\r 0-9A-Za-z]*$/.test(part.slice(end))) {
      return part.slice(0, start) + processURL(url, true) + part.slice(end);
    }
    return "#" + FAILSAFE;
  }).join(",");
}

const cssTable = {
  0: "\\0", 9: "\\9", 10: "\\a", 12: "\\c", 13: "\\d", 34: "\\22", 38: "\\26", 39: "\\27", 40: "\\28", 41: "\\29",
  43: "\\2b", 47: "\\2f", 58: "\\3a", 59: "\\3b", 60: "\\3c", 62: "\\3e", 92: "\\\\", 123: "\\7b", 125: "\\7d",
};

function cssEscaper(args) {
  const [s] = stringify(args);
  const chars = [...s];
  let out = "";
  chars.forEach((ch, i) => {
    const cp = ch.codePointAt(0);
    const repl = cssTable[cp];
    if (repl === undefined) { out += ch; return; }
    out += repl;
    const next = chars[i + 1];
    if (repl !== "\\\\" && (next === undefined || /^[0-9a-fA-F\t\n\f\r ]$/.test(next))) out += " ";
  });
  return out;
}

function cssValueFilter(args) {
  const [s, t] = stringify(args);
  if (t === "c") return s;
  if (s.includes("\\")) throw new Unsupported("CSS escapes");
  let id = "";
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if ("\0\"'()/;@[\\]`{}<>".includes(c)) return FAILSAFE;
    if (c === "-") {
      if (i !== 0 && s[i - 1] === "-") return FAILSAFE;
    } else if (/[a-zA-Z0-9_-]/.test(c)) id += c;
  }
  id = id.toLowerCase();
  if (id.includes("expression") || id.includes("mozbinding")) return FAILSAFE;
  return s;
}

const escapers = {
  _html_template_attrescaper(...a) {
    const [s, t] = stringify(a);
    if (t === "h") throw new Unsupported("HTML in an attribute (stripTags)");
    return htmlReplacer(s, htmlTable, true);
  },
  _html_template_commentescaper: () => "",
  _html_template_cssescaper: (...a) => cssEscaper(a),
  _html_template_cssvaluefilter: (...a) => cssValueFilter(a),
  _html_template_htmlnamefilter(...a) {
    const [s, t] = stringify(a);
    if (t === "ha") return s;
    throw new Unsupported("dynamic attribute names");
  },
  _html_template_htmlescaper(...a) {
    const [s, t] = stringify(a);
    return t === "h" ? s : htmlReplacer(s, htmlTable, true);
  },
  _html_template_jsregexpescaper(...a) {
    const s = replace(stringify(a)[0], jsRegexpTable);
    return s === "" ? "(?:)" : s;
  },
  _html_template_jsstrescaper(...a) {
    const [s, t] = stringify(a);
    return replace(s, t === "js" ? jsStrNormTable : jsStrTable);
  },
  _html_template_jstmpllitescaper: (...a) => replace(stringify(a)[0], jsBqTable),
  _html_template_jsvalescaper: (...a) => jsValEscaper(a),
  _html_template_nospaceescaper(...a) {
    const [s, t] = stringify(a);
    if (s === "") return FAILSAFE;
    if (t === "h") throw new Unsupported("HTML in an unquoted attribute (stripTags)");
    return htmlReplacer(s, htmlNospaceTable, false);
  },
  _html_template_rcdataescaper(...a) {
    const [s, t] = stringify(a);
    return htmlReplacer(s, t === "h" ? htmlNormTable : htmlTable, true);
  },
  _html_template_srcsetescaper: (...a) => srcsetEscaper(a),
  _html_template_urlescaper: (...a) => urlProcessor(false, a),
  _html_template_urlfilter(...a) {
    const [s, t] = stringify(a);
    if (t === "u") return s;
    return isSafeURL(s) ? s : "#" + FAILSAFE;
  },
  _html_template_urlnormalizer: (...a) => urlProcessor(true, a),
  _eval_args_(...a) {
    if (a.length === 1 && typeof a[0] === "string") return a[0];
    return sprint(a);
  },
};

// ---------------------------------------------------------------- builtins

function evalArgs(args) {
  if (args.length === 1 && typeof args[0] === "string") return args[0];
  return sprint(args);
}

function htmlEscapeString(s) {
  return s.replace(/[\0&'<>"]/g, (c) => ({ "\0": "�", "&": "&amp;", "'": "&#39;", "<": "&lt;", ">": "&gt;", '"': "&#34;" })[c]);
}

function jsEscapeString(s) {
  let out = "";
  for (const ch of s) {
    const cp = ch.codePointAt(0);
    switch (ch) {
      case "\\": out += "\\\\"; continue;
      case "'": out += "\\'"; continue;
      case '"': out += '\\"'; continue;
      case "<": out += "\\u003C"; continue;
      case ">": out += "\\u003E"; continue;
      case "&": out += "\\u0026"; continue;
      case "=": out += "\\u003D"; continue;
    }
    if (cp < 0x20) out += "\\u" + cp.toString(16).toUpperCase().padStart(4, "0");
    else if (cp > 0x7e && !/\p{L}|\p{M}|\p{N}|\p{P}|\p{S}|\p{Zs}/u.test(ch)) out += "\\u" + cp.toString(16).toUpperCase().padStart(4, "0");
    else out += ch;
  }
  return out;
}

function queryEscape(s) {
  let out = "";
  for (const c of utf8.encode(s)) {
    const ch = String.fromCharCode(c);
    if (/[A-Za-z0-9\-_.~]/.test(ch) && c < 128) out += ch;
    else if (c === 32) out += "+";
    else out += "%" + c.toString(16).toUpperCase().padStart(2, "0");
  }
  return out;
}

function basic(v) {
  if (v instanceof Stringer) return basic(v.v);
  if (v instanceof GoFloat) return v.f;
  if (v instanceof BigInt64) throw new Unsupported("comparing a 64-bit integer");
  if (v instanceof Typed) return v.s;
  if (v === NOVALUE || v === undefined) return null;
  if (v === null || typeof v !== "object") return v;
  throw new Unsupported("comparing a non-basic value");
}

function cmp(a, b) {
  a = basic(a); b = basic(b);
  if (typeof a !== typeof b || a === null) throw new Unsupported("comparing different kinds");
  if (typeof a === "string") { const x = utf8.encode(a), y = utf8.encode(b); for (let i = 0; i < Math.min(x.length, y.length); i++) if (x[i] !== y[i]) return x[i] - y[i]; return x.length - y.length; }
  if (typeof a === "number") return a - b;
  throw new Unsupported("ordering " + typeof a);
}

function byteLen(s) { return utf8.encode(s).length; }

function length(v) {
  if (v instanceof Stringer) v = v.v;
  if (typeof v === "string") return byteLen(v);
  if (v instanceof Typed) return byteLen(v.s);
  if (Array.isArray(v)) return v.length;
  if (v instanceof GoMap) return v.size;
  throw new Unsupported("len of " + typeof v);
}

function index(item, ...keys) {
  for (const k of keys) {
    if (item instanceof Stringer) item = item.v;
    if (Array.isArray(item)) {
      const i = basic(k);
      if (typeof i !== "number" || !Number.isInteger(i) || i < 0 || i >= item.length) throw new Unsupported("index out of range");
      item = item[i];
    } else if (item instanceof GoMap) {
      item = item.get(String(basic(k)));
    } else if (typeof item === "string") {
      const b = utf8.encode(item), i = basic(k);
      if (i < 0 || i >= b.length) throw new Unsupported("index out of range");
      item = b[i];
    } else throw new Unsupported("index of " + typeof item);
  }
  return item;
}

function slice(item, ...ix) {
  if (item instanceof Stringer) item = item.v;
  if (typeof item === "string") {
    if (/[^\x00-\x7f]/.test(item)) throw new Unsupported("slice of non-ASCII text");
    return item.slice(ix[0] ?? 0, ix[1] ?? item.length);
  }
  if (Array.isArray(item)) return item.slice(ix[0] ?? 0, ix[1] ?? item.length);
  throw new Unsupported("slice of " + typeof item);
}

const builtins = {
  not: (a) => !truth(a),
  len: length,
  index,
  slice,
  print: (...a) => sprint(a),
  println: (...a) => sprintln(a),
  printf: (f, ...a) => sprintf(basic(f), a),
  html: (...a) => htmlEscapeString(evalArgs(a)),
  js: (...a) => jsEscapeString(evalArgs(a)),
  urlquery: (...a) => queryEscape(evalArgs(a)),
  eq(a, ...bs) {
    if (bs.length === 0) throw new Unsupported("eq needs two arguments");
    const x = basic(a);
    return bs.some((b) => { const y = basic(b); if (x !== null && y !== null && typeof x !== typeof y) throw new Unsupported("eq of different kinds"); return x === y; });
  },
  ne: (a, b) => { const x = basic(a), y = basic(b); if (typeof x !== typeof y) throw new Unsupported("ne of different kinds"); return x !== y; },
  lt: (a, b) => cmp(a, b) < 0,
  le: (a, b) => cmp(a, b) <= 0,
  gt: (a, b) => cmp(a, b) > 0,
  ge: (a, b) => cmp(a, b) >= 0,
  dict(...kv) {
    if (kv.length % 2) throw new Unsupported("dict: odd arguments");
    const m = {};
    for (let i = 0; i < kv.length; i += 2) {
      if (typeof kv[i] !== "string") throw new Unsupported("dict key");
      m[kv[i]] = kv[i + 1];
    }
    return new GoMap(m);
  },
};

// framework functions written in Go (ClientFunc{JS: "builtin:name"})
const natives = {
  island(name, ...args) {
    if (!/^[A-Za-z0-9_-]+(\/[A-Za-z0-9_-]+)*$/.test(name)) throw new Unsupported("island name");
    let props = args.length > 0 ? args[0] : null, hydrate = "", inner = "";
    for (const a of args.slice(1)) {
      if (a instanceof Typed && a.t === "h") inner = a.s;
      else if (typeof a === "string") {
        if (a === "load" || a === "idle" || a === "visible") hydrate = a;
        else inner = htmlEscapeString(a);
      } else throw new Unsupported("island argument");
    }
    let out = '<fullstack-island data-island="' + htmlEscapeString(name) + '"';
    if (props !== null && props !== NOVALUE && props !== undefined) {
      const data = goJSON(props);
      if (data !== "null") out += ' data-props="' + htmlEscapeString(data) + '"';
    }
    if (hydrate && hydrate !== "load") out += ' data-hydrate="' + hydrate + '"';
    return new Typed("h", out + ' style="display:contents">' + inner + "</fullstack-island>");
  },
};

// ---------------------------------------------------------------- exec

const BREAK = { b: 1 }, CONTINUE = { c: 1 };

class State {
  constructor(pack, page) {
    this.pack = pack;
    this.page = page;
    this.out = [];
    this.vars = [];
    this.depth = 0;
    this.funcs = { ...builtins, ...escapers };
    for (const [name, src] of Object.entries(pack.f || {})) {
      if (src.startsWith("builtin:")) {
        const fn = natives[src.slice(8)];
        if (fn) this.funcs[name] = fn;
      } else {
        try {
          const fn = new Function("return (" + src + ")")();
          this.funcs[name] = (...a) => fn(helpers, ...a);
        } catch { /* blocked by CSP: pages using it go to the server */ }
      }
    }
  }
  varGet(name) {
    for (let i = this.vars.length - 1; i >= 0; i--) if (this.vars[i][0] === name) return this.vars[i][1];
    throw new Unsupported("undefined variable " + name);
  }
  varSet(name, v) {
    for (let i = this.vars.length - 1; i >= 0; i--) if (this.vars[i][0] === name) { this.vars[i][1] = v; return; }
    throw new Unsupported("undefined variable " + name);
  }
}

const helpers = {
  escapeHTML: htmlEscapeString,
  json: goJSON,
  html: (s) => new Typed("h", s),
  print: (v) => format(v),
};

function walk(st, dot, nodes) {
  for (const n of nodes) {
    if (typeof n === "string") { st.out.push(n); continue; }
    switch (n[0]) {
      case "A": {
        const pipe = n[1];
        const v = pipeline(st, dot, pipe);
        if (pipe[0].length === 0) st.out.push(typeof v === "string" ? v : format(v));
        break;
      }
      case "I": case "W": {
        const mark = st.vars.length;
        const v = pipeline(st, dot, n[1]);
        if (truth(v)) walk(st, n[0] === "W" ? v : dot, n[2]);
        else if (n[3]) walk(st, dot, n[3]);
        st.vars.length = mark;
        break;
      }
      case "R": rangeNode(st, dot, n); break;
      case "T": template(st, n[1], n[2] ? pipeline(st, dot, n[2]) : null); break;
      case "B": throw BREAK;
      case "C": throw CONTINUE;
      default: throw new Unsupported("node " + n[0]);
    }
  }
}

function rangeNode(st, dot, n) {
  const mark = st.vars.length;
  const [decl] = n[1];
  // the pipeline's own declaration names the element (and key), not its value
  const v = pipeline(st, dot, [[], n[1][1], n[1][2]]);
  let items;
  let val = v instanceof Stringer ? v.v : v;
  if (Array.isArray(val)) items = val.map((x, i) => [i, x]);
  else if (val instanceof GoMap) items = val.keys().map((k) => [k, val.m[k]]);
  else if (typeof val === "number" && Number.isInteger(val)) items = Array.from({ length: Math.max(val, 0) }, (_, i) => [i, i]);
  else if (val === null || val === NOVALUE || val === undefined) items = [];
  else throw new Unsupported("range over " + typeof val);
  if (items.length === 0) {
    if (n[3]) walk(st, dot, n[3]);
    st.vars.length = mark;
    return;
  }
  for (const [k, x] of items) {
    st.vars.length = mark;
    if (decl.length === 1) st.vars.push([decl[0], typeof val === "number" ? k : x]);
    else if (decl.length === 2) { st.vars.push([decl[0], k]); st.vars.push([decl[1], x]); }
    try {
      walk(st, x, n[2]);
    } catch (e) {
      if (e === BREAK) break;
      if (e !== CONTINUE) throw e;
    }
  }
  st.vars.length = mark;
}

function template(st, name, dot) {
  const id = st.page.n[name];
  if (!id) throw new Unsupported("no template " + name);
  if (++st.depth > 100) throw new Unsupported("template depth");
  const saved = st.vars;
  st.vars = [["$", dot]];
  try {
    walk(st, dot, st.pack.t[id]);
  } finally {
    st.vars = saved;
    st.depth--;
  }
}

function pipeline(st, dot, pipe) {
  const [decl, isAssign, cmds] = pipe;
  let v;
  cmds.forEach((cmd, i) => { v = command(st, dot, cmd, i === 0 ? undefined : { v }); });
  for (const name of decl) {
    if (isAssign) st.varSet(name, v);
    else st.vars.push([name, v]);
  }
  return v;
}

function command(st, dot, cmd, final) {
  const [first, ...rest] = cmd;
  switch (first[0]) {
    case "f": return fieldChain(st, dot, dot, first[1], rest, final);
    case "c": return fieldChain(st, dot, arg(st, dot, first[1]), first[2], rest, final);
    case "v": {
      const v = st.varGet(first[1][0]);
      if (first[1].length === 1) {
        if (rest.length || final) throw new Unsupported("arguments to a variable");
        return v;
      }
      return fieldChain(st, dot, v, first[1].slice(1), rest, final);
    }
    case "i": return call(st, dot, first[1], rest, final);
    case "k": {
      if (final) throw new Unsupported("constant call in a pipeline");
      if (!st.pack.k || !(first[1] in st.pack.k)) throw new Unsupported("constant");
      return decode(st.pack.k[first[1]]);
    }
    case "p":
      if (rest.length || final) throw new Unsupported("arguments to a pipeline");
      return pipeline(st, dot, first[1]);
  }
  if (rest.length || final) throw new Unsupported("arguments to a constant");
  return arg(st, dot, first);
}

function arg(st, dot, a) {
  switch (a[0]) {
    case ".": return dot;
    case "n": return null;
    case "b": case "#": case "s": return a[1];
    case "f": return fieldChain(st, dot, dot, a[1], [], undefined);
    case "c": return fieldChain(st, dot, arg(st, dot, a[1]), a[2], [], undefined);
    case "v": {
      const v = st.varGet(a[1][0]);
      return a[1].length === 1 ? v : fieldChain(st, dot, v, a[1].slice(1), [], undefined);
    }
    case "p": return pipeline(st, dot, a[1]);
    case "i": return call(st, dot, a[1], [], undefined);
    case "k": return command(st, dot, [a], undefined);
  }
  throw new Unsupported("argument " + a[0]);
}

function fieldChain(st, dot, recv, idents, rest, final) {
  idents.forEach((name, i) => {
    const last = i === idents.length - 1;
    if (last && (rest.length || final)) throw new Unsupported("method call ." + name);
    recv = field(recv, name);
  });
  return recv;
}

function field(recv, name) {
  if (recv instanceof GoMap) return recv.get(name);
  if (recv instanceof GoStruct) {
    if (Object.prototype.hasOwnProperty.call(recv.f, name)) return recv.f[name];
    throw new Unsupported("." + name + " is a method or not sent");
  }
  throw new Unsupported("." + name + " of " + (recv === null || recv === NOVALUE ? "nil" : typeof recv));
}

function call(st, dot, name, rest, final) {
  if (name === "and" || name === "or") {
    let v;
    const all = rest.map((a) => () => arg(st, dot, a));
    if (final) all.push(() => final.v);
    for (const get of all) {
      v = get();
      if (name === "and" ? !truth(v) : truth(v)) return v;
    }
    return v;
  }
  const fn = st.funcs[name];
  if (!fn) throw new Unsupported("no function " + name);
  const args = rest.map((a) => arg(st, dot, a));
  if (final) args.push(final.v);
  return fn(...args);
}

// renderPage renders a page of the pack with its data (as the server sent it).
export function renderPage(pack, view, data) {
  const page = pack.p[view];
  if (!page) throw new Unsupported("no page " + view);
  const st = new State(pack, page);
  const dot = decode(data);
  template(st, page.e, dot);
  return st.out.join("");
}
