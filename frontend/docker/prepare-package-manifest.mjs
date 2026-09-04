import { readFileSync, writeFileSync } from 'node:fs'

const platformBindings = [
  '@oxlint/binding-darwin-arm64',
  '@rolldown/binding-darwin-arm64',
]

const packagePath = '/app/package.json'
const lockPath = '/app/package-lock.json'
const packageManifest = JSON.parse(readFileSync(packagePath, 'utf8'))
const packageLock = JSON.parse(readFileSync(lockPath, 'utf8'))

for (const binding of platformBindings) {
  delete packageManifest.devDependencies?.[binding]
  delete packageLock.packages?.['']?.devDependencies?.[binding]

  const lockedPackage = packageLock.packages?.[`node_modules/${binding}`]
  if (lockedPackage) {
    lockedPackage.optional = true
  }
}

writeFileSync(packagePath, `${JSON.stringify(packageManifest, null, 2)}\n`)
writeFileSync(lockPath, `${JSON.stringify(packageLock, null, 2)}\n`)
