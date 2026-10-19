# Client Launcher

Launcher Minecraft (Fabric) multi-packs avec mises à jour automatiques.

- Connexion Microsoft, téléchargement automatique de Minecraft, Fabric et Java.
- Plusieurs packs, chacun avec son dossier de jeu.
- Bouton Serveur / Solo (connexion directe à `minecraft.nexusdescartes.com`).
- Mises à jour automatiques signées, publiées sur la branche **mises-a-jour** de ce dépôt.

## Mises à jour

Les launchers lisent :
`https://raw.githubusercontent.com/Mpixel44/client-launcher/mises-a-jour/manifest.json`

La branche contient `manifest.json`, sa signature `manifest.sig` (ed25519) et les fichiers nommés par leur empreinte SHA-256.
Seul le détenteur de la clé secrète peut produire une signature acceptée par les launchers.

## Compiler

```
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-H windowsgui -s -w" -o Launcher.exe .
```
