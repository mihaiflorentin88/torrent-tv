// Compatibility shims must execute before any shared module evaluates, so
// they are imported first; platform hooks and the AVPlay media adapter are
// installed next, before the shared TV application boots.
import './compat';
import './webos-platform';
import './avplay';
import '../../tv/src/main';
