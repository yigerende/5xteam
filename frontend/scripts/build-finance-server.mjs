import {build} from 'esbuild'
import {fileURLToPath} from 'node:url'
await build({entryPoints:[fileURLToPath(new URL('./finance-server.mjs',import.meta.url))], outfile:fileURLToPath(new URL('../../internal/financecalc/report.mjs',import.meta.url)),bundle:true,platform:'node',format:'esm',target:'node20',minify:true})
