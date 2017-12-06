.PHONY: deploy
deploy: 
	@echo "deploying to rico-ams1"
	export GOOS=linux && export GOARCH=amd64 && go build
	rsync -u top2000spotify rico-ams1:/var/www/top2000spotify/server
	rsync -ru web/. rico-ams1:/var/www/top2000spotify/web
	@echo "done! don't forget to restart the top2000spotify service"
