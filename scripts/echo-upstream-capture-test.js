const http = require('http')
const srv = http.createServer((req, res) => {
  let body = ''
  req.on('data', (c) => (body += c))
  req.on('end', () => {
    console.log('UPSTREAM', req.method, req.url, body.length)
    res.writeHead(200, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify({ ok: true, received: body.length }))
  })
})
srv.listen(5099, '127.0.0.1', () => console.log('echo listening 5099'))

