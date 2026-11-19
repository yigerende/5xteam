import {build} from 'esbuild'
await build({entryPoints:[new URL('./finance-server.mjs',import.meta.url).pathname.replace(/^\/(\w:)/,'$1')], outfile:new URL('../../internal/financecalc/report.mjs',import.meta.url).pathname.replace(/^\/(\w:)/,'$1'),bundle:true,platform:'node',format:'esm',target:'node20',minify:true})
