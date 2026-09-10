#!/bin/bash
sed -i 's/<<<<<<< HEAD//g' src/components/AppConfig/AppConfig.tsx
sed -i 's/=======//g' src/components/AppConfig/AppConfig.tsx
sed -i '/>>>>>>> origin\/pr\/6/d' src/components/AppConfig/AppConfig.tsx
