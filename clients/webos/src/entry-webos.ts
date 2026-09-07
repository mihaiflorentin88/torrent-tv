// Compatibility shims must execute before any shared module evaluates, so
// they are imported first; the shared TV application then boots unchanged.
import './compat';
import '../../tv/src/main';
