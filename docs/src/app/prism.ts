// The one place Prism and its grammars are loaded. <app-code> and <app-file>
// both import it, so a grammar is registered once and the list of languages
// the site can highlight is readable here. The Prism core already carries
// markup, css, clike and javascript; the components used to import markup
// again, and each imported yaml/bash/json on its own.
//
//   bash - the install one-liners and every `plug ...` command
//   json - the MCP configuration blocks
//   yaml - the deploy manifests served by <app-file>
//   typescript - the default `lang` of <app-code>
//   markup, kotlin, toml, makefile - the build-tool recipes (markup, for a
//          pom.xml, is in the core)
//   text - not a grammar: an unknown language leaves the block unhighlighted,
//          which is what the console transcripts want
import Prism from 'prismjs';
import 'prismjs/components/prism-bash';
import 'prismjs/components/prism-json';
import 'prismjs/components/prism-kotlin';
import 'prismjs/components/prism-makefile';
import 'prismjs/components/prism-toml';
import 'prismjs/components/prism-typescript';
import 'prismjs/components/prism-yaml';

export { Prism };
