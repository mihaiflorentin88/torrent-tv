// Compatibility shims must execute before any shared module evaluates, so
// they are imported first; platform hooks are installed next, before the
// shared TV application boots.
import './compat';
import './webos-platform';
import '../../tv/src/main';
