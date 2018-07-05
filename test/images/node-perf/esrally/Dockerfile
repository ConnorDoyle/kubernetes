# esrally container
#
# Bind-mount a volume at /home/es/.rally/benchmarks for elasticsearch
# server logs and data directory.
#
# Using java:8 because java:9 causes
# java.lang.NoSuchMethodError: java.lang.ProcessHandle.pid()J
# on launch; the release channel openjdk package appears to be broken.
FROM java:8

RUN apt-get update && \
  apt-get install -y python3-pip git && \
  rm -rf /var/lib/apt/lists/*
RUN pip3 install esrally
# Fix issue with Java crypto initialization
RUN ln -s $JAVA_HOME/lib $JAVA_HOME/conf
RUN useradd -ms /bin/bash es
USER es
WORKDIR /home/es
RUN esrally configure --java-home=$JAVA_HOME --runtime-java-home=$JAVA_HOME
RUN mkdir -p /home/es/.rally/benchmarks
ADD . /home/es
ENTRYPOINT esrally
