require "fileutils"
require 'optparse'

image_name = "router"

def exec_or_die( cmd, current_dir = nil )
	orgDir = Dir.pwd
	Dir.chdir(current_dir) if current_dir != nil
	
	res = `#{cmd}`
	
	Dir.chdir(orgDir)

	if $?.exitstatus != 0
		print(res)
		exit(false)
	end
end

options = {}
OptionParser.new do |opts|
  opts.banner = "Usage: docker-build.rb [options]"
  opts.on("-t", "--dockertag TAG", "Docker tag to use when pusing the image. Defaults to latest.") do |dt|
    options[:dockertag] = dt
  end
  opts.on("-r", "--dockerregistry REGISTRY", "The Docker registry to use. Defaults to Docker hub (imqs namesapce).") do |dr|
    options[:dockerregistry] = dr
  end
  opts.on("-u", "--dockeruser USER", "The Docker user to login with.") do |du|
    options[:dockeruser] = du
  end
  opts.on("-p", "--dockerpass PASS", "The Docker password to login with.") do |dp|
    options[:dockerpass] = dp
  end  
  opts.on("-h", "--help", "Prints this help") do
	puts opts
    exit
  end
end.parse!

puts("Building router binary")
exec_or_die("docker run --rm -e GOPATH=/usr/src/router -v #{Dir.pwd}:/usr/src/router -w /usr/src/router golang:1.8 go install -ldflags \"-linkmode external -extldflags -static\" github.com/IMQS/router-core
")
puts("Building image")
exec_or_die("docker build -t router:#{options[:dockertag]} .")

tag_command = nil
login_command = nil
push_command = nil
logout_command = nil
# supplying no registry implies pushing to docker hub
if options[:dockerregistry].nil?
	tag_command = "docker tag #{image_name}:#{options[:dockertag]} imqs/#{image_name}:#{options[:dockertag]}"
	if !options[:dockeruser].nil?
		login_command = "docker login -u #{options[:dockeruser]} -p #{options[:dockerpass]}"
		logout_command = "docker logout"
	end
	push_command = "docker push imqs/#{image_name}:#{options[:dockertag]}"
	 
else
	tag_command = "docker tag #{image_name}:#{options[:dockertag]} #{options[:dockerregistry]}/#{image_name}:#{options[:dockertag]}"
	if !options[:dockeruser].nil?
		login_command = "docker login -u #{options[:dockeruser]} -p #{options[:dockerpass]} #{options[:dockerregistry]}"
		logout_command = "docker logout #{options[:dockerregistry]}"
	end
	push_command = "docker push #{options[:dockerregistry]}/#{image_name}:#{options[:dockertag]}"	
end
puts("Taging image: #{tag_command}")
exec_or_die(tag_command)
if !login_command.nil?
	puts("Login")
	exec_or_die(login_command)
end
puts("Pusing image: #{push_command}")
exec_or_die(push_command)
if !logout_command.nil?
	puts("Logout")
	exec_or_die(logout_command)
end